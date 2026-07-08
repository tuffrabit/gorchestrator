package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	LoopCount    int    `json:"loop_count"`
	TokensUsed   int    `json:"tokens_used"`
	DurationMs   int64  `json:"duration_ms"`
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

	// Select provider.
	provider, err := buildProvider(cfg, opts.DryRun)
	if err != nil {
		return err
	}

	// Prepare paths.
	phase := "research"
	issueDir := storage.IssueDir(project.ID, issue.ID)
	allowlist := []string{issueDir}
	outputPath := storage.OutputPath(project.ID, issue.ID, phase)

	// Write task.json.
	researcher := agents.NewResearcher(provider)
	toolRegistry := tools.NewResearcherRegistry(&tools.BoundTools{})
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
		Tools:     schemasFromRegistry(toolRegistry),
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

	// Spawn agent goroutine.
	errCh := make(chan error, 1)
	go func() {
		errCh <- runAgent(ctx, cfg, store, provider, project.ID, issue.ID, opts.IssueTitle, opts.Loops, allowlist, outputPath, run, runs)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			_ = issues.UpdateStatus(issue.ID, "failed", phase)
			return err
		}
		return issues.UpdateStatus(issue.ID, "done", phase)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runAgent(ctx context.Context, cfg *config.Config, store storage.Port, provider llm.Provider, projectID, issueID int64, input string, loops int, allowlist []string, outputPath string, run *sqlite.Run, runs *sqlite.RunRepo) error {
	start := time.Now()
	totalTokens := 0

	bt := &tools.BoundTools{
		Storage:    store,
		RootPath:   cfg.StorageRoot,
		Allowlist:  allowlist,
		OutputPath: outputPath,
	}
	registry := tools.NewResearcherRegistry(bt)
	researcher := agents.NewResearcher(provider)

	for i := 1; i <= loops; i++ {
		if err := ctx.Err(); err != nil {
			_ = runs.UpdateStatus(run.ID, "failed", totalTokens, int(time.Since(start).Milliseconds()), i-1)
			return err
		}
		usage, err := researcher.Run(ctx, input, registry)
		if err != nil {
			_ = runs.UpdateStatus(run.ID, "failed", totalTokens, int(time.Since(start).Milliseconds()), i)
			return fmt.Errorf("loop %d: %w", i, err)
		}
		totalTokens += usage.TotalTokens
	}

	duration := time.Since(start).Milliseconds()
	result := Result{
		Status:     "done",
		LoopCount:  loops,
		TokensUsed: totalTokens,
		DurationMs: duration,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	resultData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := store.Write(ctx, storage.ResultPath(projectID, issueID, "research"), resultData); err != nil {
		return fmt.Errorf("write result.json: %w", err)
	}
	return runs.UpdateStatus(run.ID, "done", totalTokens, int(duration), loops)
}

func schemasFromRegistry(r *tools.Registry) []map[string]any {
	llmTools := r.LLMTools()
	out := make([]map[string]any, 0, len(llmTools))
	for _, t := range llmTools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		})
	}
	return out
}

func buildProvider(cfg *config.Config, dryRun bool) (llm.Provider, error) {
	if dryRun {
		return llm.NewDryRunProvider(), nil
	}
	switch cfg.DefaultModel.Provider {
	case "openai":
		return llm.NewOpenAIProvider(cfg.DefaultModel.Model, cfg.DefaultModel.APIKeyEnv, "", cfg.DefaultModel.TimeoutDur), nil
	case "local":
		return llm.NewLocalProvider(cfg.DefaultModel.Model, cfg.DefaultModel.APIKeyEnv, "", cfg.DefaultModel.TimeoutDur), nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.DefaultModel.Provider)
	}
}
