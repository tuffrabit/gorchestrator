package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/tuffrabit/gorchestrator/internal/adapters"
	"github.com/tuffrabit/gorchestrator/internal/agents"
	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/tools"
)

// RunOptions holds the CLI flags for a run.
type RunOptions struct {
	ProjectName string
	IssueTitle  string
	DryRun      bool
	Loops       int
}

// Result is the orchestrator-written status envelope.
type Result struct {
	Status        string `json:"status"`
	Error         string `json:"error"`
	LoopCount     int    `json:"loop_count"`
	TokensUsed    int    `json:"tokens_used"`
	DurationMs    int64  `json:"duration_ms"`
	DoneRationale string `json:"done_rationale"`
	Timestamp     string `json:"timestamp"`
}

// Task is the orchestrator-written instructions/config file.
type Task struct {
	AgentType    string            `json:"agent_type"`
	SystemPrompt string            `json:"system_prompt"`
	Model        map[string]string `json:"model"`
	LoopMode     string            `json:"loop_mode"`
	NLoops       int               `json:"n_loops"`
	Input        string            `json:"input"`
	Allowlist    []string          `json:"allowlist"`
	Tools        []map[string]any  `json:"tools"`
}

// Run executes the full researcher run lifecycle.
func Run(ctx context.Context, cfg *config.Config, opts RunOptions) error {
	if opts.Loops <= 0 {
		opts.Loops = 1
	}

	// Storage.
	store, err := storage.NewFS(cfg.StorageRoot)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}

	// SQLite.
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()

	projects := sqlite.NewProjectRepo(db)
	issues := sqlite.NewIssueRepo(db)
	runs := sqlite.NewRunRepo(db)

	project, err := projects.GetOrCreate(opts.ProjectName)
	if err != nil {
		return fmt.Errorf("get or create project: %w", err)
	}
	issue, err := issues.Create(project.ID, opts.IssueTitle)
	if err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	// Discover external adapters (best-effort).
	_, _ = adapters.Discovery(cfg.AdaptersDir)

	// Select model.
	modelCfg := llm.Config{
		Provider:  cfg.DefaultModel.Provider,
		Model:     cfg.DefaultModel.Model,
		APIKeyEnv: cfg.DefaultModel.APIKeyEnv,
		BaseURL:   cfg.DefaultModel.BaseURL,
		Timeout:   cfg.DefaultModel.TimeoutDur,
	}
	if opts.DryRun {
		modelCfg.Provider = "dryrun"
		modelCfg.Model = "dryrun"
	}
	llmModel, err := llm.New(ctx, modelCfg)
	if err != nil {
		return fmt.Errorf("build model: %w", err)
	}

	// Prepare paths.
	phase := "research"
	issueDir := storage.IssueDir(project.ID, issue.ID)
	allowlist := []string{issueDir}
	outputPath := storage.OutputPath(project.ID, issue.ID, phase)

	// Build agent and capture tool schemas for task.json.
	researcher := agents.NewResearcher()
	toolSchemas := schemasFromTools(tools.NewResearcherRegistry(&tools.BoundTools{}))
	task := Task{
		AgentType:    "researcher",
		SystemPrompt: researcher.SystemPrompt,
		Model: map[string]string{
			"provider": cfg.DefaultModel.Provider,
			"model":    cfg.DefaultModel.Model,
		},
		LoopMode:  "n_loops",
		NLoops:    opts.Loops,
		Input:     opts.IssueTitle,
		Allowlist: allowlist,
		Tools:     toolSchemas,
	}
	taskData, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	if err := store.Write(ctx, storage.TaskPath(project.ID, issue.ID, phase), taskData); err != nil {
		return fmt.Errorf("write task.json: %w", err)
	}

	// Create run record.
	run, err := runs.Create(issue.ID, "researcher", cfg.DefaultModel.Model, "in_progress")
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}

	// Run the agent for n_loops, each loop in a fresh ADK session.
	start := time.Now()
	totalTokens := 0
	lastOutput := ""
	var runErr error

	for i := 1; i <= opts.Loops; i++ {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}

		outputWritten := false
		bt := &tools.BoundTools{
			Storage:       store,
			RootPath:      cfg.StorageRoot,
			Allowlist:     allowlist,
			OutputPath:    outputPath,
			OutputWritten: &outputWritten,
		}
		registry := tools.NewResearcherRegistry(bt)
		agentInst, err := researcher.Build(llmModel, registry)
		if err != nil {
			runErr = fmt.Errorf("build researcher: %w", err)
			break
		}

		sessionID := fmt.Sprintf("run-%d-loop-%d", run.ID, i)
		r, err := runner.New(runner.Config{
			AppName:           "gorchestrator",
			Agent:             agentInst,
			SessionService:    session.InMemoryService(),
			AutoCreateSession: true,
		})
		if err != nil {
			runErr = fmt.Errorf("create runner: %w", err)
			break
		}

		loopStart := time.Now()
		loopTokens := 0
		userContent := genai.NewContentFromText(opts.IssueTitle, genai.RoleUser)

		var finalText string
		for ev, err := range r.Run(ctx, "user", sessionID, userContent, agent.RunConfig{}) {
			if err != nil {
				runErr = fmt.Errorf("loop %d: %w", i, err)
				break
			}
			if ev == nil || ev.Content == nil {
				continue
			}
			if ev.UsageMetadata != nil {
				loopTokens = int(ev.UsageMetadata.TotalTokenCount)
			}
			if ev.Content.Role == genai.RoleModel {
				for _, p := range ev.Content.Parts {
					if p != nil && p.Text != "" {
						finalText = p.Text
					}
				}
			}
		}
		if runErr != nil {
			break
		}

		// If the agent did not use write_output, fall back to the final model text.
		if !outputWritten {
			if err := store.Write(ctx, outputPath, []byte(finalText)); err != nil {
				runErr = fmt.Errorf("write fallback output: %w", err)
				break
			}
		}

		lastOutput = finalText
		totalTokens += loopTokens
		_ = loopStart
	}

	duration := time.Since(start).Milliseconds()

	status := "done"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		errMsg = runErr.Error()
	}

	result := Result{
		Status:     status,
		Error:      errMsg,
		LoopCount:  opts.Loops,
		TokensUsed: totalTokens,
		DurationMs: duration,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	if lastOutput != "" {
		result.DoneRationale = lastOutput
	}

	resultData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := store.Write(ctx, storage.ResultPath(project.ID, issue.ID, phase), resultData); err != nil {
		return fmt.Errorf("write result.json: %w", err)
	}

	if runErr != nil {
		_ = issues.UpdateStatus(issue.ID, "failed", phase)
		_ = runs.UpdateStatus(run.ID, "failed", totalTokens, int(duration), opts.Loops)
		return runErr
	}

	_ = issues.UpdateStatus(issue.ID, "done", phase)
	return runs.UpdateStatus(run.ID, "done", totalTokens, int(duration), opts.Loops)
}

// schemasFromTools returns JSON-serializable tool schemas for task.json.
// Because ADK infers schemas from Go struct types, we construct a minimal
// declaration by reflecting over the function tool declarations.
func schemasFromTools(toolList []tool.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(toolList))
	for _, t := range toolList {
		declarer, ok := t.(interface{ Declaration() *genai.FunctionDeclaration })
		if !ok {
			continue
		}
		decl := declarer.Declaration()
		out = append(out, map[string]any{
			"name":        decl.Name,
			"description": decl.Description,
			"parameters":  schemaToMap(decl.Parameters),
		})
	}
	return out
}

func finishTaskResult(output any) string {
	m, ok := output.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m["result"]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func schemaToMap(s *genai.Schema) map[string]any {
	if s == nil {
		return nil
	}
	m := map[string]any{
		"type": s.Type,
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if s.Format != "" {
		m["format"] = s.Format
	}
	if s.Nullable != nil && *s.Nullable {
		m["nullable"] = true
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Items != nil {
		m["items"] = schemaToMap(s.Items)
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for k, v := range s.Properties {
			props[k] = schemaToMap(v)
		}
		m["properties"] = props
	}
	return m
}
