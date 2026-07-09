package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"iter"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/tuffrabit/gorchestrator/internal/adjudication"
	"github.com/tuffrabit/gorchestrator/internal/agents"
	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/tools"
)

// PhaseResult is the orchestrator-written status envelope for a phase.
type PhaseResult struct {
	Status        string `json:"status"`
	Error         string `json:"error"`
	Attempt       int    `json:"attempt"`
	LoopCount     int    `json:"loop_count"`
	TokensUsed    int    `json:"tokens_used"`
	DurationMs    int64  `json:"duration_ms"`
	DoneRationale string `json:"done_rationale"`
	LatestOutput  string `json:"latest_output"`
	Timestamp     string `json:"timestamp"`
}

// PhaseTask is the orchestrator-written instructions/config file for a phase.
type PhaseTask struct {
	AgentType         string            `json:"agent_type"`
	SystemPrompt      string            `json:"system_prompt"`
	Model             map[string]string `json:"model"`
	Adjudicator       string            `json:"adjudicator"`
	MaxAttempts       int               `json:"max_attempts"`
	Loops             int               `json:"loops"`
	InputContextPaths []string          `json:"input_context_paths"`
	Allowlist         []string          `json:"allowlist"`
	Tools             []map[string]any  `json:"tools"`
}

// eventRecord is a single line in events.jsonl.
type eventRecord struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Attempt   int            `json:"attempt"`
	Loop      int            `json:"loop"`
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	ToolCall  map[string]any `json:"tool_call,omitempty"`
	ToolResult map[string]any `json:"tool_result,omitempty"`
	Tokens    int            `json:"tokens,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// Engine executes the multi-agent pipeline.
type Engine struct {
	cfg       *config.Config
	store     storage.Port
	db        *sql.DB
	projects  *sqlite.ProjectRepo
	issues    *sqlite.IssueRepo
	runs      *sqlite.RunRepo
	decisions *sqlite.DecisionRepo
}

// NewEngine creates an engine from configuration.
func NewEngine(cfg *config.Config) (*Engine, error) {
	store, err := storage.NewFS(cfg.StorageRoot)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return &Engine{
		cfg:       cfg,
		store:     store,
		db:        db,
		projects:  sqlite.NewProjectRepo(db),
		issues:    sqlite.NewIssueRepo(db),
		runs:      sqlite.NewRunRepo(db),
		decisions: sqlite.NewDecisionRepo(db),
	}, nil
}

// Close releases engine resources.
func (e *Engine) Close() error {
	if e.db != nil {
		return e.db.Close()
	}
	return nil
}

// RunOptions holds the CLI flags for a new run.
type RunOptions struct {
	ProjectName string
	IssueTitle  string
	SourcePath  string
	DryRun      bool
}

// Run creates a new issue and executes the full pipeline.
func (e *Engine) Run(ctx context.Context, opts RunOptions) error {
	project, err := e.projects.GetOrCreate(opts.ProjectName)
	if err != nil {
		return fmt.Errorf("get or create project: %w", err)
	}

	if opts.SourcePath != "" {
		if err := e.setProjectSourcePath(project, opts.SourcePath); err != nil {
			return fmt.Errorf("set project source path: %w", err)
		}
	}

	sourcePath, err := e.projectSourcePath(project)
	if err != nil {
		return err
	}

	issue, err := e.issues.Create(project.ID, opts.IssueTitle)
	if err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	if sourcePath != "" {
		if err := e.snapshotSource(ctx, project.ID, issue.ID, sourcePath); err != nil {
			return fmt.Errorf("snapshot source: %w", err)
		}
	}

	return e.runPipeline(ctx, project, issue, opts.DryRun)
}

// ResumeOptions holds the CLI flags for resuming an issue.
type ResumeOptions struct {
	ProjectName string
	IssueID     int64
	Decision    string
	Feedback    string
}

// Resume continues an existing issue, applying a human decision if one is pending.
func (e *Engine) Resume(ctx context.Context, opts ResumeOptions) error {
	project, err := e.projects.GetByName(opts.ProjectName)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	if project == nil {
		return fmt.Errorf("project %q not found", opts.ProjectName)
	}

	issue, err := e.issues.Get(opts.IssueID)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}
	if issue == nil || issue.ProjectID != project.ID {
		return fmt.Errorf("issue %d not found in project %q", opts.IssueID, opts.ProjectName)
	}

	phase, status, err := e.currentPhaseState(project.ID, issue.ID)
	if err != nil {
		return err
	}

	switch status {
	case "waiting_human":
		if opts.Decision == "" {
			return fmt.Errorf("phase %s is waiting for a decision; provide --decision=pass|fail|retry", phase)
		}
		if err := e.applyHumanDecision(ctx, project, issue, phase, opts.Decision, opts.Feedback); err != nil {
			return err
		}
	case "in_progress":
		// Crash recovery: re-run the current phase.
		log.Printf("recovering crashed phase %s for issue %d", phase, issue.ID)
	case "failed", "cancelled":
		return fmt.Errorf("issue %d phase %s is %s; cannot resume", issue.ID, phase, status)
	case "done":
		// Continue to next phase.
	}

	return e.runPipeline(ctx, project, issue, false)
}

// runPipeline executes research -> plan -> implementation.
func (e *Engine) runPipeline(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, dryRun bool) error {
	phases := []string{"research", "plan", "implementation"}

	for _, phaseName := range phases {
		phaseCfg := e.cfg.Agent(phaseAgentType(phaseName))

		currentPhase, status, err := e.currentPhaseState(project.ID, issue.ID)
		if err != nil {
			return err
		}

		// currentPhaseState returns the first non-done phase. If it reports a
		// phase ahead of the one we expect, that earlier phase is already done.
		phaseIdx := indexOf(phases, phaseName)
		currentIdx := indexOf(phases, currentPhase)
		if currentIdx > phaseIdx {
			continue
		}

		// If currentPhaseState points at an earlier phase than our loop variable,
		// use the current phase so we pick up the correct config and input.
		if currentIdx < phaseIdx {
			phaseName = currentPhase
			phaseCfg = e.cfg.Agent(phaseAgentType(phaseName))
		}

		switch status {
		case "done":
			continue
		case "waiting_human":
			_ = e.issues.UpdateStatus(issue.ID, "waiting_human", phaseName)
			return fmt.Errorf("phase %s is waiting for human decision; use resume", phaseName)
		case "failed", "cancelled":
			_ = e.issues.UpdateStatus(issue.ID, status, phaseName)
			return fmt.Errorf("phase %s %s", phaseName, status)
		}

		baseInput, err := e.buildBaseInput(ctx, project.ID, issue.ID, phaseName, issue.Title)
		if err != nil {
			return fmt.Errorf("build input for %s: %w", phaseName, err)
		}

		result, err := e.runPhase(ctx, project, issue, phaseName, phaseCfg, baseInput, dryRun)
		if err != nil {
			return fmt.Errorf("run phase %s: %w", phaseName, err)
		}

		_ = e.issues.UpdateStatus(issue.ID, result.Status, phaseName)

		switch result.Status {
		case "done":
			continue
		case "waiting_human":
			return nil
		default:
			return fmt.Errorf("phase %s %s: %s", phaseName, result.Status, result.Error)
		}
	}

	_ = e.issues.UpdateStatus(issue.ID, "done", "implementation")
	return nil
}

// runPhase runs a single phase with adjudication attempts.
func (e *Engine) runPhase(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phase string, cfg config.AgentConfig, baseInput string, dryRun bool) (*PhaseResult, error) {
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	loops := cfg.Loops
	if loops <= 0 {
		loops = 1
	}

	sourceSnapshotPath := storage.SourcePath(project.ID, issue.ID)

	adjudicator := adjudication.New(cfg.Adjudicator)

	// Resume from the previous state if present: a retry continues at the next
	// attempt, while an in-progress result (crash) re-runs the same attempt.
	resultPath := storage.ResultPath(project.ID, issue.ID, phase)
	previous, _ := readResult(ctx, e.store, resultPath)
	startAttempt := 1
	if previous.Status == "retry" && previous.Attempt > 0 {
		startAttempt = previous.Attempt + 1
	}

	for attempt := startAttempt; attempt <= maxAttempts; attempt++ {
		// Implementation phases need a clean workspace seeded from source.
		if phase == "implementation" {
			if err := e.seedWorkspace(ctx, project.ID, issue.ID, sourceSnapshotPath); err != nil {
				return nil, fmt.Errorf("seed workspace: %w", err)
			}
		}

		input := baseInput
		if attempt > 1 {
			retryCtx, err := e.buildRetryContext(ctx, project.ID, issue.ID, phase, attempt-1)
			if err != nil {
				return nil, fmt.Errorf("build retry context: %w", err)
			}
			input += retryCtx
		}

		outputPath := storage.AttemptOutputPath(project.ID, issue.ID, phase, attempt)
		eventsPath := storage.EventsPath(project.ID, issue.ID, phase)

		// Write in-progress result.json at phase start.
		start := time.Now()
		if err := writeResult(ctx, e.store, resultPath, PhaseResult{
			Status:    "in_progress",
			Attempt:   attempt,
			LoopCount: 0,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return nil, fmt.Errorf("write in-progress result: %w", err)
		}

		// Build task.json.
		task, err := e.buildTask(ctx, project.ID, issue.ID, phase, cfg, attempt, outputPath, dryRun)
		if err != nil {
			return nil, fmt.Errorf("build task: %w", err)
		}
		taskData, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal task: %w", err)
		}
		if err := e.store.Write(ctx, storage.TaskPath(project.ID, issue.ID, phase), taskData); err != nil {
			return nil, fmt.Errorf("write task.json: %w", err)
		}

		// Create run record.
		modelName := cfg.Model.Model
		if dryRun {
			modelName = "dryrun"
		}
		run, err := e.runs.Create(issue.ID, phaseAgentType(phase), modelName, "in_progress")
		if err != nil {
			return nil, fmt.Errorf("create run: %w", err)
		}

		allowlist := []string{
			storage.IssueDir(project.ID, issue.ID),
			sourceSnapshotPath,
		}
		if phase == "implementation" {
			allowlist = append(allowlist, storage.WorkspacePath(project.ID, issue.ID))
		}

		totalTokens := 0
		loopCount := 0
		var loopErr error
		var doneRationale string
		var phaseDone bool = true

		for loop := 1; loop <= loops; loop++ {
			if err := ctx.Err(); err != nil {
				loopErr = err
				loopCount = loop
				break
			}

			loopInput := e.buildLoopInput(ctx, input, outputPath, loop)
			output, done, rationale, tokens, err := e.runAgentLoop(ctx, project.ID, issue.ID, phase, cfg, loopInput, outputPath, eventsPath, allowlist, attempt, loop, run.ID, dryRun)
			if err != nil {
				loopErr = err
				loopCount = loop
				break
			}
			loopCount = loop
			totalTokens += tokens
			if rationale != "" {
				doneRationale = rationale
			}
			phaseDone = done
			_ = output
		}

		duration := time.Since(start).Milliseconds()

		status := "done"
		errMsg := ""
		if loopErr != nil {
			if ctx.Err() == context.Canceled {
				status = "cancelled"
			} else {
				status = "failed"
			}
			errMsg = loopErr.Error()
		}

		latestOutput := ""
		if exists, _ := e.store.Exists(ctx, outputPath); exists {
			latestOutput = outputPath
		}

		result := &PhaseResult{
			Status:        status,
			Error:         errMsg,
			Attempt:       attempt,
			LoopCount:     loopCount,
			TokensUsed:    totalTokens,
			DurationMs:    duration,
			DoneRationale: doneRationale,
			LatestOutput:  latestOutput,
			Timestamp:     time.Now().UTC().Format(time.RFC3339),
		}

		if err := writeResult(ctx, e.store, resultPath, *result); err != nil {
			return nil, fmt.Errorf("write result.json: %w", err)
		}
		_ = e.runs.UpdateStatus(run.ID, status, totalTokens, int(duration), loopCount)

		if loopErr != nil {
			return result, nil
		}

		// Apply adjudicator at the boundary.
		decision, err := adjudicator.Evaluate(ctx, phase, adjudication.Attempt{
			Output:        nil,
			Done:          phaseDone,
			DoneRationale: doneRationale,
		})
		if err != nil {
			return nil, fmt.Errorf("adjudicate: %w", err)
		}

		switch decision.Outcome {
		case adjudication.Pass:
			result.Status = "done"
			result.DoneRationale = doneRationale
			if err := writeResult(ctx, e.store, resultPath, *result); err != nil {
				return nil, err
			}
			return result, nil
		case adjudication.Fail:
			result.Status = "failed"
			result.Error = decision.Feedback
			if err := writeResult(ctx, e.store, resultPath, *result); err != nil {
				return nil, err
			}
			return result, nil
		case adjudication.Retry:
			if attempt < maxAttempts {
				feedbackPath := storage.FeedbackPath(project.ID, issue.ID, phase, attempt)
				if err := e.store.Write(ctx, feedbackPath, []byte(decision.Feedback)); err != nil {
					return nil, fmt.Errorf("write feedback: %w", err)
				}
				result.Status = "retry"
				result.Error = decision.Feedback
				if err := writeResult(ctx, e.store, resultPath, *result); err != nil {
					return nil, err
				}
				continue
			}
			result.Status = "failed"
			result.Error = "max attempts exceeded: " + decision.Feedback
			if err := writeResult(ctx, e.store, resultPath, *result); err != nil {
				return nil, err
			}
			return result, nil
		case adjudication.WaitingHuman:
			result.Status = "waiting_human"
			if err := writeResult(ctx, e.store, resultPath, *result); err != nil {
				return nil, err
			}
			if _, err := e.decisions.Create(issue.ID, phase); err != nil {
				log.Printf("failed to record pending decision: %v", err)
			}
			return result, nil
		}
	}

	return &PhaseResult{Status: "failed", Error: "max attempts exceeded"}, nil
}

// runAgentLoop runs one loop of an agent and returns the loop output, the
// finish_task done flag, rationale, token count, and any error.
func (e *Engine) runAgentLoop(ctx context.Context, projectID, issueID int64, phase string, cfg config.AgentConfig, userContent *genai.Content, outputPath, eventsPath string, allowlist []string, attempt, loop int, runID int64, dryRun bool) ([]byte, bool, string, int, error) {
	outputWritten := false
	bt := &tools.BoundTools{
		Storage:          e.store,
		RootPath:         e.cfg.StorageRoot,
		Allowlist:        allowlist,
		OutputPath:       outputPath,
		ReadFileMaxBytes: e.cfg.Tools.ReadFile.MaxBytes,
		ReadFileMaxLines: e.cfg.Tools.ReadFile.MaxLines,
		OutputWritten:    &outputWritten,
	}
	if phase == "implementation" {
		bt.WorkspacePath = storage.WorkspacePath(projectID, issueID)
	}

	var registry []tool.Tool
	var err error
	switch phase {
	case "research", "plan":
		registry, err = tools.NewResearcherRegistry(bt)
	case "implementation":
		registry, err = tools.NewImplementerRegistry(bt)
	}
	if err != nil {
		return nil, false, "", 0, fmt.Errorf("build tool registry: %w", err)
	}

	modelCfg := llm.Config{
		Provider:  cfg.Model.Provider,
		Model:     cfg.Model.Model,
		APIKeyEnv: cfg.Model.APIKeyEnv,
		BaseURL:   cfg.Model.BaseURL,
		Timeout:   modelTimeout(cfg.Model),
	}
	if dryRun {
		modelCfg.Provider = "dryrun"
		modelCfg.Model = "dryrun"
	}
	llmModel, err := llm.New(ctx, modelCfg)
	if err != nil {
		return nil, false, "", 0, fmt.Errorf("build model: %w", err)
	}

	agentInst, err := e.buildAgent(phase, cfg, llmModel, registry)
	if err != nil {
		return nil, false, "", 0, fmt.Errorf("build agent: %w", err)
	}

	wrapper, err := agent.New(agent.Config{
		Name:        phaseAgentType(phase) + "-runner",
		Description: "wrapper to run a task-mode agent through the runner",
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return llmagent.RunLLMAgentAsNode(agentInst, agent.NewContext(ctx), ctx.UserContent())
		},
	})
	if err != nil {
		return nil, false, "", 0, fmt.Errorf("create agent wrapper: %w", err)
	}

	sessionID := fmt.Sprintf("run-%d-attempt-%d-loop-%d", runID, attempt, loop)
	r, err := runner.New(runner.Config{
		AppName:           "gorchestrator",
		Agent:             wrapper,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, false, "", 0, fmt.Errorf("create runner: %w", err)
	}

	loopTokens := 0
	var finishRationale string
	var finishDone *bool
	var finalText string

	for ev, err := range r.Run(ctx, "user", sessionID, userContent, agent.RunConfig{}) {
		if err != nil {
			return nil, false, "", 0, fmt.Errorf("loop %d: %w", loop, err)
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
					recordEvent(ctx, e.store, eventsPath, eventRecord{
						Type:      "tool_call",
						Timestamp: time.Now().UTC().Format(time.RFC3339),
						Attempt:   attempt,
						Loop:      loop,
						ToolCall: map[string]any{
							"id":   p.FunctionCall.ID,
							"name": p.FunctionCall.Name,
							"args": p.FunctionCall.Args,
						},
					})
					if p.FunctionCall.Name == "finish_task" {
						if args := p.FunctionCall.Args; args != nil {
							if r, ok := args["rationale"].(string); ok {
								finishRationale = r
							}
							if d, ok := args["done"].(bool); ok {
								finishDone = &d
							}
						}
					}
				}
			}
			recordEvent(ctx, e.store, eventsPath, eventRecord{
				Type:      "model_turn",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Attempt:   attempt,
				Loop:      loop,
				Role:      string(ev.Content.Role),
				Content:   cappedText(finalText),
			})
		} else if ev.Content.Role == genai.RoleUser {
			for _, p := range ev.Content.Parts {
				if p == nil || p.FunctionResponse == nil {
					continue
				}
				recordEvent(ctx, e.store, eventsPath, eventRecord{
					Type:      "tool_result",
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Attempt:   attempt,
					Loop:      loop,
					ToolResult: map[string]any{
						"id":       p.FunctionResponse.ID,
						"name":     p.FunctionResponse.Name,
						"response": p.FunctionResponse.Response,
					},
				})
			}
		}
	}

	if loopTokens > 0 {
		recordEvent(ctx, e.store, eventsPath, eventRecord{
			Type:      "usage",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Attempt:   attempt,
			Loop:      loop,
			Tokens:    loopTokens,
		})
	}

	// If the agent did not use write_output, fall back to the final model text.
	if !outputWritten && finalText != "" {
		if err := e.store.Write(ctx, outputPath, []byte(finalText)); err != nil {
			return nil, false, "", 0, fmt.Errorf("write fallback output: %w", err)
		}
	}

	// For the implementer, the real output is the workspace; create a summary
	// output.md if the agent did not write one.
	if phase == "implementation" {
		if exists, _ := e.store.Exists(ctx, outputPath); !exists {
			summary := fmt.Sprintf("Implementation complete.\n\nRationale: %s", finishRationale)
			if err := e.store.Write(ctx, outputPath, []byte(summary)); err != nil {
				return nil, false, "", 0, fmt.Errorf("write implementation summary: %w", err)
			}
		}
	}

	output, _ := e.store.Read(ctx, outputPath)
	if phase != "implementation" && !outputWritten && finalText == "" && len(output) == 0 {
		return nil, false, "", 0, fmt.Errorf("loop %d produced empty output", loop)
	}

	if finishDone == nil {
		return nil, false, "", 0, fmt.Errorf("loop %d did not call finish_task", loop)
	}

	return output, *finishDone, finishRationale, loopTokens, nil
}

// buildAgent constructs the ADK agent for a phase.
func (e *Engine) buildAgent(phase string, cfg config.AgentConfig, llmModel adkmodel.LLM, tools []tool.Tool) (agent.Agent, error) {
	switch phase {
	case "research":
		return agents.NewResearcher().Build(llmModel, tools)
	case "plan":
		return agents.NewPlanner().Build(llmModel, tools)
	case "implementation":
		return agents.NewImplementer().Build(llmModel, tools)
	default:
		return nil, fmt.Errorf("unknown phase: %s", phase)
	}
}

// buildTask constructs the task.json content for a phase.
func (e *Engine) buildTask(ctx context.Context, projectID, issueID int64, phase string, cfg config.AgentConfig, attempt int, outputPath string, dryRun bool) (PhaseTask, error) {
	agentType := phaseAgentType(phase)
	model := map[string]string{
		"provider": cfg.Model.Provider,
		"model":    cfg.Model.Model,
	}
	if dryRun {
		model["provider"] = "dryrun"
		model["model"] = "dryrun"
	}

	var toolList []tool.Tool
	bt := &tools.BoundTools{}
	switch phase {
	case "research", "plan":
		toolList, _ = tools.NewResearcherRegistry(bt)
	case "implementation":
		toolList, _ = tools.NewImplementerRegistry(bt)
	}
	toolSchemas, err := schemasFromTools(toolList)
	if err != nil {
		return PhaseTask{}, err
	}

	allowlist := []string{
		storage.IssueDir(projectID, issueID),
		storage.SourcePath(projectID, issueID),
	}
	if phase == "implementation" {
		allowlist = append(allowlist, storage.WorkspacePath(projectID, issueID))
	}

	inputPaths := []string{fmt.Sprintf("Issue title: <issue %d>", issueID)}
	if phase != "research" {
		prev := previousPhase(phase)
		if prev != "" {
			res, err := readResult(ctx, e.store, storage.ResultPath(projectID, issueID, prev))
			if err == nil && res.LatestOutput != "" {
				inputPaths = append(inputPaths, res.LatestOutput)
			}
		}
	}

	return PhaseTask{
		AgentType:         agentType,
		SystemPrompt:      cfg.SystemPrompt,
		Model:             model,
		Adjudicator:       cfg.Adjudicator,
		MaxAttempts:       cfg.MaxAttempts,
		Loops:             cfg.Loops,
		InputContextPaths: inputPaths,
		Allowlist:         allowlist,
		Tools:             toolSchemas,
	}, nil
}

// buildBaseInput composes the issue plus the previous accepted phase output.
func (e *Engine) buildBaseInput(ctx context.Context, projectID, issueID int64, phase, issueTitle string) (string, error) {
	input := fmt.Sprintf("Issue: %s", issueTitle)
	prev := previousPhase(phase)
	if prev == "" {
		return input, nil
	}
	res, err := readResult(ctx, e.store, storage.ResultPath(projectID, issueID, prev))
	if err != nil {
		return input, nil
	}
	if res.Status != "done" || res.LatestOutput == "" {
		return input, nil
	}
	data, err := e.store.Read(ctx, res.LatestOutput)
	if err != nil || len(data) == 0 {
		return input, nil
	}
	input += fmt.Sprintf("\n\nAccepted %s output:\n%s", prev, string(data))
	return input, nil
}

// buildRetryContext appends the rejected attempt's output and feedback.
func (e *Engine) buildRetryContext(ctx context.Context, projectID, issueID int64, phase string, prevAttempt int) (string, error) {
	outputPath := storage.AttemptOutputPath(projectID, issueID, phase, prevAttempt)
	feedbackPath := storage.FeedbackPath(projectID, issueID, phase, prevAttempt)

	var parts []string
	parts = append(parts, fmt.Sprintf("\n\nRejected attempt %d output:", prevAttempt))
	if data, err := e.store.Read(ctx, outputPath); err == nil && len(data) > 0 {
		parts = append(parts, string(data))
	} else {
		parts = append(parts, "(no output)")
	}
	parts = append(parts, "Adjudicator feedback:")
	if data, err := e.store.Read(ctx, feedbackPath); err == nil && len(data) > 0 {
		parts = append(parts, string(data))
	} else {
		parts = append(parts, "(no feedback)")
	}
	return strings.Join(parts, "\n"), nil
}

// buildLoopInput feeds the previous loop's output into the current loop.
func (e *Engine) buildLoopInput(ctx context.Context, baseInput, outputPath string, loop int) *genai.Content {
	text := baseInput
	if loop > 1 {
		prev, err := e.store.Read(ctx, outputPath)
		if err == nil && len(prev) > 0 {
			text = fmt.Sprintf("%s\n\nPrevious loop output:\n%s", baseInput, string(prev))
		}
	}
	return genai.NewContentFromText(text, genai.RoleUser)
}

// applyHumanDecision applies a resume decision to a waiting_human phase.
func (e *Engine) applyHumanDecision(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phase, decisionStr, feedback string) error {
	decision := adjudication.ParseOutcome(decisionStr)
	resultPath := storage.ResultPath(project.ID, issue.ID, phase)
	result, err := readResult(ctx, e.store, resultPath)
	if err != nil {
		return fmt.Errorf("read result for human decision: %w", err)
	}

	// Record the decision in SQLite.
	if err := e.decisions.Record(issue.ID, phase, decisionStr, feedback, "cli"); err != nil {
		log.Printf("failed to record decision: %v", err)
	}

	switch decision {
	case adjudication.Pass:
		result.Status = "done"
		result.DoneRationale = feedback
		result.Timestamp = time.Now().UTC().Format(time.RFC3339)
		return writeResult(ctx, e.store, resultPath, result)
	case adjudication.Fail:
		result.Status = "failed"
		result.Error = feedback
		result.Timestamp = time.Now().UTC().Format(time.RFC3339)
		return writeResult(ctx, e.store, resultPath, result)
	case adjudication.Retry:
		// Mark as retry; the phase machine will start a new attempt.
		result.Status = "retry"
		result.Error = feedback
		result.Timestamp = time.Now().UTC().Format(time.RFC3339)
		feedbackPath := storage.FeedbackPath(project.ID, issue.ID, phase, result.Attempt)
		if err := e.store.Write(ctx, feedbackPath, []byte(feedback)); err != nil {
			return fmt.Errorf("write feedback: %w", err)
		}
		return writeResult(ctx, e.store, resultPath, result)
	default:
		return fmt.Errorf("invalid decision %q", decisionStr)
	}
}

// currentPhaseState returns the current phase and its status from the filesystem.
func (e *Engine) currentPhaseState(projectID, issueID int64) (string, string, error) {
	phases := []string{"research", "plan", "implementation"}
	for _, phase := range phases {
		result, err := readResult(context.Background(), e.store, storage.ResultPath(projectID, issueID, phase))
		if err != nil {
			// No result.json yet means this phase hasn't run.
			return phase, "in_progress", nil
		}
		switch result.Status {
		case "done":
			continue
		case "":
			return phase, "in_progress", nil
		default:
			return phase, result.Status, nil
		}
	}
	return "implementation", "done", nil
}

// snapshotSource copies sourcePath into the issue's source snapshot directory.
func (e *Engine) snapshotSource(ctx context.Context, projectID, issueID int64, sourcePath string) error {
	dest := storage.SourcePath(projectID, issueID)
	return copyDirToStorage(ctx, e.store, sourcePath, dest)
}

// seedWorkspace copies the source snapshot into the implementation workspace.
// If the source snapshot does not exist (no project source configured), the
// workspace is left empty.
func (e *Engine) seedWorkspace(ctx context.Context, projectID, issueID int64, sourceSnapshotPath string) error {
	exists, err := e.store.Exists(ctx, sourceSnapshotPath)
	if err != nil {
		return fmt.Errorf("check source snapshot: %w", err)
	}
	if !exists {
		return nil
	}
	dest := storage.WorkspacePath(projectID, issueID)
	return copyStorageDir(ctx, e.store, sourceSnapshotPath, dest)
}

// setProjectSourcePath stores or updates the project's source path in config_json.
func (e *Engine) setProjectSourcePath(project *sqlite.Project, sourcePath string) error {
	cfg, err := e.projectConfig(project)
	if err != nil {
		return err
	}
	cfg["source_path"] = sourcePath
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal project config: %w", err)
	}
	_, err = e.db.Exec(`UPDATE projects SET config_json = ? WHERE id = ?`, string(data), project.ID)
	if err != nil {
		return fmt.Errorf("update project config: %w", err)
	}
	project.ConfigJSON = string(data)
	return nil
}

// projectSourcePath extracts the source path from project config_json.
func (e *Engine) projectSourcePath(project *sqlite.Project) (string, error) {
	cfg, err := e.projectConfig(project)
	if err != nil {
		return "", err
	}
	if v, ok := cfg["source_path"].(string); ok {
		return v, nil
	}
	return "", nil
}

// projectConfig parses a project's config_json into a map.
func (e *Engine) projectConfig(project *sqlite.Project) (map[string]any, error) {
	cfg := map[string]any{}
	if project.ConfigJSON == "" || project.ConfigJSON == "{}" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(project.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
	}
	return cfg, nil
}

// copyDirToStorage copies a host directory into the storage port under destKey.
func copyDirToStorage(ctx context.Context, store storage.Port, srcDir, destKey string) error {
	absSrc, err := filepath.Abs(srcDir)
	if err != nil {
		return fmt.Errorf("resolve source path: %w", err)
	}
	info, err := os.Stat(absSrc)
	if err != nil {
		return fmt.Errorf("stat source path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path is not a directory: %s", srcDir)
	}

	return filepath.WalkDir(absSrc, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(absSrc, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, ".git/") || rel == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		key := path.Join(destKey, rel)
		if err := store.Write(ctx, key, data); err != nil {
			return fmt.Errorf("write %s: %w", key, err)
		}
		return nil
	})
}

// copyStorageDir copies one storage directory to another via the storage port.
func copyStorageDir(ctx context.Context, store storage.Port, srcKey, destKey string) error {
	if err := store.Mkdir(ctx, destKey); err != nil {
		return fmt.Errorf("mkdir %s: %w", destKey, err)
	}

	entries, err := listRecursive(ctx, store, srcKey)
	if err != nil {
		return fmt.Errorf("list %s: %w", srcKey, err)
	}

	for _, entry := range entries {
		data, err := store.Read(ctx, entry)
		if err != nil {
			return fmt.Errorf("read %s: %w", entry, err)
		}
		rel, _ := filepath.Rel(srcKey, entry)
		dest := path.Join(destKey, filepath.ToSlash(rel))
		if err := store.Write(ctx, dest, data); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
	}
	return nil
}

// listRecursive returns all file paths under key using the storage port.
func listRecursive(ctx context.Context, store storage.Port, key string) ([]string, error) {
	entries, err := store.List(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		child := path.Join(key, e.Name)
		if e.IsDir {
			sub, err := listRecursive(ctx, store, child)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		out = append(out, child)
	}
	return out, nil
}

// phaseAgentType maps a phase name to its agent type.
func indexOf(sl []string, s string) int {
	for i, v := range sl {
		if v == s {
			return i
		}
	}
	return -1
}

func phaseAgentType(phase string) string {
	switch phase {
	case "research":
		return "researcher"
	case "plan":
		return "planner"
	case "implementation":
		return "implementer"
	default:
		return phase
	}
}

// previousPhase returns the phase that feeds into the given phase.
func previousPhase(phase string) string {
	switch phase {
	case "plan":
		return "research"
	case "implementation":
		return "plan"
	default:
		return ""
	}
}

func modelTimeout(cfg config.ModelConfig) time.Duration {
	if cfg.Timeout == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		return 60 * time.Second
	}
	return d
}

func writeResult(ctx context.Context, store storage.Port, path string, result PhaseResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return store.Write(ctx, path, data)
}

func readResult(ctx context.Context, store storage.Port, path string) (PhaseResult, error) {
	var result PhaseResult
	data, err := store.Read(ctx, path)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
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

// schemasFromTools returns JSON-serializable tool schemas for task.json.
func schemasFromTools(toolList []tool.Tool) ([]map[string]any, error) {
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
