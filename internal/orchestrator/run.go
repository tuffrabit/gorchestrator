package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	Status       string `json:"status"`
	Error        string `json:"error"`
	Attempt      int    `json:"attempt"`
	LoopCount    int    `json:"loop_count"`
	TokensUsed   int    `json:"tokens_used"`
	DurationMs   int64  `json:"duration_ms"`
	LatestOutput string `json:"latest_output"`
	DoneRationale string `json:"done_rationale"`
	Timestamp    string `json:"timestamp"`
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

// eventRecord is a single line in events.jsonl.
type eventRecord struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Loop      int            `json:"loop"`
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	ToolCall  map[string]any `json:"tool_call,omitempty"`
	Tokens    int            `json:"tokens,omitempty"`
	Error     string         `json:"error,omitempty"`
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

	// Load configured adapters (explicit registry).
	loadAdapters(cfg)

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
	attempt := 1
	issueDir := storage.IssueDir(project.ID, issue.ID)
	allowlist := []string{issueDir}
	outputPath := storage.AttemptOutputPath(project.ID, issue.ID, phase, attempt)
	eventsPath := storage.EventsPath(project.ID, issue.ID, phase)
	resultPath := storage.ResultPath(project.ID, issue.ID, phase)

	// Build agent and capture tool schemas for task.json.
	researcher := agents.NewResearcher()
	toolSchemas, err := schemasFromTools(tools.NewResearcherRegistry(&tools.BoundTools{}))
	if err != nil {
		return fmt.Errorf("build tool schemas: %w", err)
	}
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

	// Write in-progress result.json at phase start.
	start := time.Now()
	if err := writeResult(ctx, store, resultPath, Result{
		Status:    "in_progress",
		Attempt:   attempt,
		LoopCount: 0,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("write in-progress result: %w", err)
	}

	// Create run record.
	run, err := runs.Create(issue.ID, "researcher", cfg.DefaultModel.Model, "in_progress")
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}

	totalTokens := 0
	loopCount := 0
	var runErr error

	for i := 1; i <= opts.Loops; i++ {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}

		outputWritten := false
		bt := &tools.BoundTools{
			Storage:          store,
			RootPath:         cfg.StorageRoot,
			Allowlist:        allowlist,
			OutputPath:       outputPath,
			ReadFileMaxBytes: cfg.Tools.ReadFile.MaxBytes,
			ReadFileMaxLines: cfg.Tools.ReadFile.MaxLines,
			OutputWritten:    &outputWritten,
		}
		registry, err := tools.NewResearcherRegistry(bt)
		if err != nil {
			runErr = fmt.Errorf("build researcher registry: %w", err)
			break
		}
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

		loopTokens := 0
		userContent := buildLoopUserContent(ctx, store, opts.IssueTitle, outputPath, i)

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
				loopTokens += int(ev.UsageMetadata.TotalTokenCount)
			}
			if ev.Content.Role == genai.RoleModel {
				for _, p := range ev.Content.Parts {
					if p == nil {
						continue
					}
					if p.Text != "" {
						finalText = p.Text
					}
					if p.FunctionCall != nil {
						recordEvent(ctx, store, eventsPath, eventRecord{
							Type:      "tool_call",
							Timestamp: time.Now().UTC().Format(time.RFC3339),
							Loop:      i,
							ToolCall: map[string]any{
								"id":   p.FunctionCall.ID,
								"name": p.FunctionCall.Name,
								"args": p.FunctionCall.Args,
							},
						})
					}
				}
			}
			recordEvent(ctx, store, eventsPath, eventRecord{
				Type:      "model_turn",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Loop:      i,
				Role:      string(ev.Content.Role),
				Content:   cappedText(finalText),
			})
		}
		if runErr != nil {
			loopCount = i
			break
		}

		// If the agent did not use write_output, fall back to the final model text.
		if !outputWritten {
			if finalText == "" {
				runErr = fmt.Errorf("loop %d produced empty output", i)
				loopCount = i
				break
			}
			if err := store.Write(ctx, outputPath, []byte(finalText)); err != nil {
				runErr = fmt.Errorf("write fallback output: %w", err)
				loopCount = i
				break
			}
		}

		loopCount = i
		totalTokens += loopTokens
		recordEvent(ctx, store, eventsPath, eventRecord{
			Type:      "usage",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Loop:      i,
			Tokens:    loopTokens,
		})
	}

	duration := time.Since(start).Milliseconds()

	status := "done"
	errMsg := ""
	if runErr != nil {
		if ctx.Err() == context.Canceled {
			status = "cancelled"
		} else {
			status = "failed"
		}
		errMsg = runErr.Error()
	}

	latestOutput := ""
	if exists, _ := store.Exists(ctx, outputPath); exists {
		latestOutput = outputPath
	}

	result := Result{
		Status:       status,
		Error:        errMsg,
		Attempt:      attempt,
		LoopCount:    loopCount,
		TokensUsed:   totalTokens,
		DurationMs:   duration,
		LatestOutput: latestOutput,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeResult(ctx, store, resultPath, result); err != nil {
		return fmt.Errorf("write result.json: %w", err)
	}

	if runErr != nil {
		_ = issues.UpdateStatus(issue.ID, status, phase)
		_ = runs.UpdateStatus(run.ID, status, totalTokens, int(duration), loopCount)
		return runErr
	}

	_ = issues.UpdateStatus(issue.ID, "done", phase)
	return runs.UpdateStatus(run.ID, "done", totalTokens, int(duration), loopCount)
}

func writeResult(ctx context.Context, store storage.Port, path string, result Result) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return store.Write(ctx, path, data)
}

func buildLoopUserContent(ctx context.Context, store storage.Port, issueTitle, outputPath string, loop int) *genai.Content {
	var text string
	if loop > 1 {
		prev, err := store.Read(ctx, outputPath)
		if err == nil && len(prev) > 0 {
			text = fmt.Sprintf("Issue: %s\n\nPrevious loop output:\n%s", issueTitle, string(prev))
		}
	}
	if text == "" {
		text = issueTitle
	}
	return genai.NewContentFromText(text, genai.RoleUser)
}

func recordEvent(ctx context.Context, store storage.Port, path string, ev eventRecord) {
	line, err := json.Marshal(ev)
	if err != nil {
		log.Printf("failed to marshal event: %v", err)
		return
	}
	line = append(line, '\n')

	existing, _ := store.Read(ctx, path)
	data := append(existing, line...)
	if err := store.Write(ctx, path, data); err != nil {
		log.Printf("failed to write event: %v", err)
	}
}

func cappedText(s string) string {
	const maxEventBytes = 4096
	if len(s) <= maxEventBytes {
		return s
	}
	return s[:maxEventBytes] + "\n... [truncated]"
}

func loadAdapters(cfg *config.Config) map[string]*adapters.Manifest {
	loaded := make(map[string]*adapters.Manifest)
	for _, ac := range cfg.Adapters {
		m, err := adapters.LoadManifest(ac.ManifestPath)
		if err != nil {
			log.Printf("failed to load adapter %q: %v", ac.Name, err)
			continue
		}
		loaded[ac.Name] = m
		log.Printf("loaded adapter %q (%s) from %s", m.Name, m.Port, ac.ManifestPath)
	}
	return loaded
}

// schemasFromTools returns JSON-serializable tool schemas for task.json.
// Because ADK infers schemas from Go struct types, we construct a minimal
// declaration by reflecting over the function tool declarations.
func schemasFromTools(toolList []tool.Tool, err error) ([]map[string]any, error) {
	if err != nil {
		return nil, err
	}
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
			"parameters":  llm.SchemaToMap(decl.Parameters),
		})
	}
	return out, nil
}
