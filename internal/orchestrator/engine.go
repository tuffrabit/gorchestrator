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
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/tuffrabit/gorchestrator/internal/adapters"
	"github.com/tuffrabit/gorchestrator/internal/adjudication"
	"github.com/tuffrabit/gorchestrator/internal/agents"
	"github.com/tuffrabit/gorchestrator/internal/config"
	gorchgit "github.com/tuffrabit/gorchestrator/internal/git"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	gorchmcp "github.com/tuffrabit/gorchestrator/internal/mcp"
	"github.com/tuffrabit/gorchestrator/internal/notify"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/tools"
	"github.com/tuffrabit/gorchestrator/internal/trigger"
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
	users     *sqlite.UserRepo
	sessions  *sqlite.SessionRepo
	audit     *sqlite.AuditRepo
	notifs    *sqlite.NotificationRepo
	bus       *EventBus
	notifier  *notify.Dispatcher
	mcp       *gorchmcp.Manager
}

// NewEngine creates an engine from configuration.
func NewEngine(cfg *config.Config) (*Engine, error) {
	store, err := openStorage(cfg)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	e := &Engine{
		cfg:       cfg,
		store:     store,
		db:        db,
		projects:  sqlite.NewProjectRepo(db),
		issues:    sqlite.NewIssueRepo(db),
		runs:      sqlite.NewRunRepo(db),
		decisions: sqlite.NewDecisionRepo(db),
		users:     sqlite.NewUserRepo(db),
		sessions:  sqlite.NewSessionRepo(db),
		audit:     sqlite.NewAuditRepo(db),
		notifs:    sqlite.NewNotificationRepo(db),
		bus:       NewEventBus(),
	}
	if err := e.SyncProjects(); err != nil {
		_ = e.Close()
		return nil, fmt.Errorf("sync projects: %w", err)
	}
	return e, nil
}

// SyncProjects upserts the SQLite project registry from cfg.Projects (YAML).
// Missing names are created; existing names have config_json refreshed.
// Projects present only in SQLite are left in place (historical issues) but
// cannot accept new work until listed in YAML.
func (e *Engine) SyncProjects() error {
	if e.cfg.Projects == nil {
		return nil
	}
	for name, pc := range e.cfg.Projects {
		data, err := json.Marshal(pc)
		if err != nil {
			return fmt.Errorf("marshal project %q: %w", name, err)
		}
		existing, err := e.projects.GetByName(name)
		if err != nil {
			return fmt.Errorf("get project %q: %w", name, err)
		}
		if existing == nil {
			if _, err := e.projects.CreateWithConfig(name, string(data)); err != nil {
				return fmt.Errorf("create project %q: %w", name, err)
			}
			continue
		}
		if err := e.projects.UpdateConfigJSON(existing.ID, string(data)); err != nil {
			return fmt.Errorf("update project %q: %w", name, err)
		}
		existing.ConfigJSON = string(data)
	}
	return nil
}

// resolveRegisteredProject requires name ∈ YAML projects map and a synced DB row.
func (e *Engine) resolveRegisteredProject(name string) (*sqlite.Project, error) {
	if name == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if e.cfg.Projects == nil {
		return nil, fmt.Errorf("unknown project %q: no projects declared in config", name)
	}
	if _, ok := e.cfg.Projects[name]; !ok {
		return nil, fmt.Errorf("unknown project %q: not declared in config projects map", name)
	}
	p, err := e.projects.GetByName(name)
	if err != nil {
		return nil, fmt.Errorf("get project %q: %w", name, err)
	}
	if p == nil {
		return nil, fmt.Errorf("project %q registered in config but missing from database; restart to sync", name)
	}
	return p, nil
}

// ProjectConfig returns the YAML project config for a name, if registered.
func (e *Engine) ProjectConfig(name string) (config.ProjectConfig, bool) {
	if e.cfg.Projects == nil {
		return config.ProjectConfig{}, false
	}
	pc, ok := e.cfg.Projects[name]
	return pc, ok
}

// RegisteredProjectNames returns sorted names from the YAML projects map.
func (e *Engine) RegisteredProjectNames() []string {
	if e.cfg.Projects == nil {
		return nil
	}
	names := make([]string, 0, len(e.cfg.Projects))
	for n := range e.cfg.Projects {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ListRegisteredProjects returns synced project rows for names present in YAML,
// each paired with its flavor catalog for the submit UI.
func (e *Engine) ListRegisteredProjects(ctx context.Context) ([]RegisteredProject, error) {
	_ = ctx
	names := e.RegisteredProjectNames()
	out := make([]RegisteredProject, 0, len(names))
	for _, name := range names {
		p, err := e.projects.GetByName(name)
		if err != nil {
			return nil, err
		}
		if p == nil {
			continue
		}
		pc := e.cfg.Projects[name]
		out = append(out, RegisteredProject{
			Project: p,
			Agents:  pc.FlavorCatalog(),
		})
	}
	return out, nil
}

// RegisteredProject is a YAML-registered project with its flavor catalog.
type RegisteredProject struct {
	Project *sqlite.Project
	Agents  map[string]config.AgentFlavorInfo
}

func openStorage(cfg *config.Config) (storage.Port, error) {
	backend := cfg.Storage.Backend
	if backend == "" || backend == "fs" {
		return storage.NewFS(cfg.StorageRoot)
	}
	if backend != "adapter" {
		return nil, fmt.Errorf("unknown storage.backend %q", backend)
	}
	name := cfg.Storage.AdapterName
	if name == "" {
		return nil, fmt.Errorf("storage.adapter_name required when backend=adapter")
	}
	for _, ac := range cfg.Adapters {
		if ac.Name != name {
			continue
		}
		m, err := adapters.LoadManifest(ac.ManifestPath)
		if err != nil {
			return nil, fmt.Errorf("load storage adapter %s: %w", name, err)
		}
		if m.Port != "storage" {
			return nil, fmt.Errorf("adapter %s port=%s, want storage", name, m.Port)
		}
		sup, err := adapters.NewSupervisor(m.Binary, adapters.SupervisorConfig{})
		if err != nil {
			return nil, fmt.Errorf("start storage adapter %s: %w", name, err)
		}
		return storage.NewRPCPort(sup, name), nil
	}
	return nil, fmt.Errorf("storage adapter %q not found in adapters:", name)
}

// Cfg returns the engine configuration.
func (e *Engine) Cfg() *config.Config {
	return e.cfg
}

// DB returns the underlying database handle.
func (e *Engine) DB() *sql.DB {
	return e.db
}

// Users returns the user repository.
func (e *Engine) Users() *sqlite.UserRepo {
	return e.users
}

// Sessions returns the session repository.
func (e *Engine) Sessions() *sqlite.SessionRepo {
	return e.sessions
}

// Audit returns the audit repository.
func (e *Engine) Audit() *sqlite.AuditRepo {
	return e.audit
}

// Notifications returns the notification repository.
func (e *Engine) Notifications() *sqlite.NotificationRepo {
	return e.notifs
}

// SetNotifier attaches a notification dispatcher (console + optional adapters).
func (e *Engine) SetNotifier(d *notify.Dispatcher) {
	e.notifier = d
}

// SetMCP attaches an MCP manager (per-agent server allowlists).
func (e *Engine) SetMCP(m *gorchmcp.Manager) {
	e.mcp = m
}

// Close releases engine resources.
func (e *Engine) Close() error {
	if e.mcp != nil {
		_ = e.mcp.Close()
	}
	if e.db != nil {
		return e.db.Close()
	}
	return nil
}

// RunOptions holds parameters for a new run or queue submit.
type RunOptions struct {
	ProjectName string
	IssueTitle  string
	// Description is optional longer issue text (UI: Description; API/triggers: body).
	// Persisted to SQLite and issue.md together via persistIssueContext.
	Description string
	// Attachments are optional text-like files included in issue context.
	Attachments []AttachmentFile
	DryRun      bool
	// Source is the trigger provenance: manual | webhook | github | jira | ...
	Source string
	// ExternalID is an optional id from the external system.
	ExternalID string
	// TrustExternal skips the forced human implementer gate for external sources.
	TrustExternal bool
	// AgentFlavors is an optional cast: map agent type → flavor name.
	// Missing keys are filled from the project's default when flavors exist.
	AgentFlavors map[string]string
}

// Run creates a new issue and executes the full pipeline.
func (e *Engine) Run(ctx context.Context, opts RunOptions) error {
	project, err := e.resolveRegisteredProject(opts.ProjectName)
	if err != nil {
		return err
	}

	castJSON, err := e.resolveAndMarshalCast(project.Name, opts.AgentFlavors)
	if err != nil {
		return err
	}

	issue, err := e.issues.CreateWithCast(project.ID, opts.IssueTitle, castJSON)
	if err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	if err := e.persistIssueContext(ctx, issue, opts.IssueTitle, opts.Description, opts.Attachments); err != nil {
		return fmt.Errorf("persist issue context: %w", err)
	}

	if err := e.prepareIssueSource(ctx, project, issue); err != nil {
		return fmt.Errorf("prepare source: %w", err)
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
// Unlike the daemon path, Resume runs the pipeline synchronously in-process.
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
	case "waiting_human", "failed", "cancelled":
		// Human may intervene on gates and on failed/cancelled phases (inject
		// feedback and retry, or pass/fail).
		if opts.Decision == "" {
			return fmt.Errorf("phase %s is %s; provide --decision=pass|fail|retry", phase, status)
		}
		if err := e.Decide(ctx, DecideOptions{
			IssueID:   issue.ID,
			Decision:  opts.Decision,
			Feedback:  opts.Feedback,
			Phase:     phase,
			DecidedBy: "cli",
		}); err != nil {
			return err
		}
		issue, err = e.issues.Get(opts.IssueID)
		if err != nil || issue == nil {
			return fmt.Errorf("reload issue after decide: %w", err)
		}
		switch issue.Status {
		case sqlite.StatusDone, sqlite.StatusFailed, sqlite.StatusCancelled:
			return nil
		case sqlite.StatusQueued:
			// Sync CLI path: run immediately instead of waiting for workers.
			_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusInProgress, issue.CurrentPhase)
			issue.Status = sqlite.StatusInProgress
		}
	case "in_progress":
		// Crash recovery: re-run the current phase.
		log.Printf("recovering crashed phase %s for issue %d", phase, issue.ID)
	case "done":
		// Continue to next phase.
	case "retry":
		// Human already marked retry on disk; continue pipeline.
	}

	return e.runPipeline(ctx, project, issue, issue.DryRun)
}

// agentConfigForIssue returns agent config with project flavor cast and
// untrusted-input defaults applied.
func (e *Engine) agentConfigForIssue(project *sqlite.Project, issue *sqlite.Issue, phaseName string) (config.AgentConfig, error) {
	agentType := phaseAgentType(phaseName)
	phaseCfg := e.cfg.Agent(agentType)

	pc, err := e.typedProjectConfig(project)
	if err != nil {
		return phaseCfg, err
	}
	cast := parseIssueCast(issue.AgentFlavorsJSON)
	flavorName := cast[agentType]
	overlay, ok, err := pc.FlavorOverlay(agentType, flavorName)
	if err != nil {
		return phaseCfg, err
	}
	if ok {
		phaseCfg = config.MergeAgent(phaseCfg, overlay)
	}

	// Untrusted external issues default to human gate before implementation.
	if phaseName == "implementation" && trigger.IsExternal(issue.Source) {
		trust := e.cfg.Triggers.TrustExternal || pc.TrustExternal
		if !trust {
			phaseCfg.Adjudicator = "human"
		}
	}
	return phaseCfg, nil
}

func (e *Engine) resolveAndMarshalCast(projectName string, requested map[string]string) (string, error) {
	pc, ok := e.cfg.Projects[projectName]
	if !ok {
		return "{}", nil
	}
	cast, err := pc.ResolveCast(requested)
	if err != nil {
		return "", err
	}
	if len(cast) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(cast)
	if err != nil {
		return "", fmt.Errorf("marshal agent flavors: %w", err)
	}
	return string(data), nil
}

func parseIssueCast(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" || raw == "{}" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// runPipeline executes research -> plan -> implementation.
func (e *Engine) runPipeline(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, dryRun bool) error {
	phases := []string{"research", "plan", "implementation"}

	for _, phaseName := range phases {
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
		}
		phaseCfg, err := e.agentConfigForIssue(project, issue, phaseName)
		if err != nil {
			return fmt.Errorf("agent config for %s: %w", phaseName, err)
		}

		switch status {
		case "done":
			continue
		case "waiting_human":
			_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusWaitingHuman, phaseName)
			e.Publish(Event{
				Type: EventDecisionRequested, IssueID: issue.ID, ProjectID: project.ID,
				Phase: phaseName, Status: sqlite.StatusWaitingHuman,
			})
			return fmt.Errorf("phase %s is waiting for human decision; use resume", phaseName)
		case "failed", "cancelled":
			// Stale terminal FS state without a prior Decide→retry conversion.
			// Do not auto-rerun; humans must decide (which rewrites result to retry).
			_ = e.issues.UpdateStatus(issue.ID, status, phaseName)
			return fmt.Errorf("phase %s %s; use decide/retry with feedback to re-run", phaseName, status)
		case "retry":
			// Fall through and re-run the phase (human or adjudicator requested retry).
		}

		// Issue status is independent of phase result.json status. Mark the
		// issue in_progress on the new phase *before* work starts so SSE/UI
		// can show the transition (research → plan → implementation).
		_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusInProgress, phaseName)
		e.Publish(Event{
			Type: EventPhaseStarted, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phaseName, Status: sqlite.StatusInProgress,
		})

		baseInput, err := e.buildBaseInput(ctx, project.ID, issue.ID, phaseName, issue.Title, issue.Description)
		if err != nil {
			return fmt.Errorf("build input for %s: %w", phaseName, err)
		}

		result, err := e.runPhase(ctx, project, issue, phaseName, phaseCfg, baseInput, dryRun)
		if err != nil {
			return fmt.Errorf("run phase %s: %w", phaseName, err)
		}

		issueStatus := mapPhaseResultToIssueStatus(result.Status)
		// On phase success, point current_phase at the *next* stage immediately so
		// the dashboard shows the transition before the next phase_started lands.
		phaseForIssue := phaseName
		if result.Status == "done" {
			if next := nextPhaseName(phaseName); next != "" {
				phaseForIssue = next
			}
		}
		_ = e.issues.UpdateStatus(issue.ID, issueStatus, phaseForIssue)
		e.Publish(Event{
			Type: EventPhaseFinished, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phaseName, Status: issueStatus, Message: result.Error,
			Data: map[string]any{
				"phase_result":  result.Status,
				"current_phase": phaseForIssue,
			},
		})

		switch result.Status {
		case "done":
			// Pipeline continues; issue stays in_progress until all phases finish.
			continue
		case "waiting_human":
			e.Publish(Event{
				Type: EventDecisionRequested, IssueID: issue.ID, ProjectID: project.ID,
				Phase: phaseName, Status: sqlite.StatusWaitingHuman,
			})
			notify.NotifyHumanGate(ctx, e.notifier, issue.ID, phaseName, project.Name, issue.Title, e.adminEmails())
			return nil
		default:
			notify.NotifyBadOutput(ctx, e.notifier, issue.ID, phaseName, result.Error, e.adminEmails())
			return fmt.Errorf("phase %s %s: %s", phaseName, result.Status, result.Error)
		}
	}

	_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusDone, "implementation")
	e.Publish(Event{
		Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
		Phase: "implementation", Status: sqlite.StatusDone,
	})
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

	adjudicator := adjudication.New(cfg.Adjudicator)

	// Resume from the previous state if present: a retry continues at the next
	// attempt, while an in-progress result (crash) re-runs the same attempt.
	resultPath := storage.ResultPath(project.ID, issue.ID, phase)
	previous, _ := readResult(ctx, e.store, resultPath)
	startAttempt := 1
	if previous.Status == "retry" && previous.Attempt > 0 {
		startAttempt = previous.Attempt + 1
	}
	// Human retries may exceed MaxAttempts — the human is the gate. Allow at
	// least the next attempt so operators can inject feedback after a failure.
	limit := maxAttempts
	if startAttempt > limit {
		limit = startAttempt
	}

	for attempt := startAttempt; attempt <= limit; attempt++ {
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

		// Create run record before workspace setup so branch names can use run_id.
		modelName := cfg.Model.Model
		if dryRun {
			modelName = "dryrun"
		}
		run, err := e.runs.Create(issue.ID, phaseAgentType(phase), modelName, "in_progress")
		if err != nil {
			return nil, fmt.Errorf("create run: %w", err)
		}

		// Implementation phases need a clean workspace (git worktree or snapshot copy).
		if phase == "implementation" {
			if err := e.prepareImplementerWorkspace(ctx, project, issue, run); err != nil {
				return nil, fmt.Errorf("prepare workspace: %w", err)
			}
		}

		allowlist := []string{
			storage.IssueDir(project.ID, issue.ID),
			storage.SourcePath(project.ID, issue.ID),
		}
		if phase == "implementation" {
			allowlist = append(allowlist, storage.WorkspacePath(project.ID, issue.ID))
		}

		// Agents need explicit path guidance; short names like "source" only
		// work once BasePath/allowlist resolution is documented in context.
		input = input + pathGuide(project.ID, issue.ID, phase, allowlist)

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

		// After a successful implementer run, create the single structured commit.
		if phase == "implementation" {
			if err := e.commitImplementerWorkspace(ctx, project, issue, run); err != nil {
				log.Printf("git commit after implementer: %v", err)
				result.Status = "failed"
				result.Error = "git commit: " + err.Error()
				_ = writeResult(ctx, e.store, resultPath, *result)
				_ = e.runs.UpdateStatus(run.ID, "failed", totalTokens, int(duration), loopCount)
				return result, nil
			}
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
			if attempt < limit {
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
	issueDir := storage.IssueDir(projectID, issueID)
	bt := &tools.BoundTools{
		Storage:          e.store,
		RootPath:         e.cfg.StorageRoot,
		Allowlist:        allowlist,
		BasePath:         issueDir,
		OutputPath:       outputPath,
		ReadFileMaxBytes: e.cfg.Tools.ReadFile.MaxBytes,
		ReadFileMaxLines: e.cfg.Tools.ReadFile.MaxLines,
		OutputWritten:    &outputWritten,
	}
	if phase == "implementation" {
		wsKey := storage.WorkspacePath(projectID, issueID)
		bt.WorkspacePath = wsKey
		bt.WorkspaceHostPath = storage.Abs(e.cfg.StorageRoot, wsKey)
		// Default short paths (list ".", read "main.go") to the workspace.
		bt.BasePath = wsKey
		// Resolve project for test config (issue → project).
		if issue, ierr := e.issues.Get(issueID); ierr == nil && issue != nil {
			if project, perr := e.projects.Get(issue.ProjectID); perr == nil && project != nil {
				tc, _ := e.projectTestConfig(project)
				if dryRun {
					tc.DryRun = true
				}
				if tc.Command != "" {
					bt.Test = &tc
				}
			}
		}
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
	registry = tools.FilterByNames(registry, cfg.Tools)
	if e.mcp != nil && len(cfg.MCPServers) > 0 {
		mcpTools, merr := e.mcp.ToolsForAgent(cfg.MCPServers)
		if merr != nil {
			return nil, false, "", 0, fmt.Errorf("mcp tools: %w", merr)
		}
		registry = append(registry, mcpTools...)
	}

	modelCfg := llm.Config{
		Provider:    cfg.Model.Provider,
		Model:       cfg.Model.Model,
		APIKeyEnv:   cfg.Model.APIKeyEnv,
		BaseURL:     cfg.Model.BaseURL,
		Timeout:     modelTimeout(cfg.Model),
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
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
		a := agents.NewResearcher()
		if cfg.SystemPrompt != "" {
			a.SystemPrompt = cfg.SystemPrompt
		}
		return a.Build(llmModel, tools)
	case "plan":
		a := agents.NewPlanner()
		if cfg.SystemPrompt != "" {
			a.SystemPrompt = cfg.SystemPrompt
		}
		return a.Build(llmModel, tools)
	case "implementation":
		a := agents.NewImplementer()
		if cfg.SystemPrompt != "" {
			a.SystemPrompt = cfg.SystemPrompt
		}
		return a.Build(llmModel, tools)
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
	toolList = tools.FilterByNames(toolList, cfg.Tools)
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

	inputPaths := []string{storage.IssueMarkdownPath(projectID, issueID)}
	if names, err := e.listAttachmentNames(ctx, projectID, issueID); err == nil {
		for _, name := range names {
			inputPaths = append(inputPaths, storage.AttachmentPath(projectID, issueID, name))
		}
	}
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

// buildBaseInput composes the issue (title, description, attachment paths)
// plus the previous accepted phase output.
func (e *Engine) buildBaseInput(ctx context.Context, projectID, issueID int64, phase, issueTitle, description string) (string, error) {
	// Prefer SQLite description; fall back to empty if unset (legacy issues).
	names, _ := e.listAttachmentNames(ctx, projectID, issueID)
	input := buildIssueUserInput(issueTitle, description, names)
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

// pathGuide tells the agent how tool paths work for this issue. Without it,
// models guess names like "attempts" or "." against the storage root and
// loop on "path not allowed".
func pathGuide(projectID, issueID int64, phase string, allowlist []string) string {
	issueDir := storage.IssueDir(projectID, issueID)
	source := storage.SourcePath(projectID, issueID)
	var b strings.Builder
	b.WriteString("\n\n## Path guide for tools\n")
	b.WriteString("Tool paths are relative to the issue directory (short names like `source`), ")
	b.WriteString("or full storage keys under the allowlist. Path traversal (`..`) is rejected.\n")
	b.WriteString(fmt.Sprintf("- Issue root (`list_directory` with path omitted or `.`): `%s`\n", issueDir))
	b.WriteString(fmt.Sprintf("- Source snapshot (start research/plan here): `%s` or `source`\n", source))
	if phase == "implementation" {
		ws := storage.WorkspacePath(projectID, issueID)
		b.WriteString(fmt.Sprintf("- Workspace (default base for implementer; edit here): `%s`\n", ws))
	}
	b.WriteString("- Allowlist prefixes:\n")
	for _, a := range allowlist {
		b.WriteString(fmt.Sprintf("  - `%s`\n", a))
	}
	return b.String()
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

// applyHumanDecision applies a resume decision to a waiting_human phase (CLI helper).
func (e *Engine) applyHumanDecision(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phase, decisionStr, feedback string) error {
	return e.applyHumanDecisionWithBy(ctx, project, issue, phase, decisionStr, feedback, "cli")
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func (e *Engine) adminEmails() []string {
	admins, err := e.users.ListAdmins()
	if err != nil || len(admins) == 0 {
		return e.cfg.Auth.BootstrapAdminEmails
	}
	out := make([]string, 0, len(admins))
	for _, a := range admins {
		out = append(out, a.Email)
	}
	return out
}

// CurrentPhaseState returns the current phase and its status from the filesystem.
func (e *Engine) CurrentPhaseState(projectID, issueID int64) (string, string, error) {
	return e.currentPhaseState(projectID, issueID)
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

// prepareIssueSource sets up the issue's source/ tree: git worktree when
// configured, otherwise a Phase 2 snapshot copy from source_path.
func (e *Engine) prepareIssueSource(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue) error {
	gitCfg, err := e.projectGitConfig(project)
	if err != nil {
		return err
	}
	if gitCfg.Enabled() {
		return e.prepareGitSource(ctx, project.ID, issue.ID, gitCfg)
	}
	sourcePath, err := e.projectSourcePath(project)
	if err != nil {
		return err
	}
	if sourcePath == "" {
		return nil
	}
	return e.snapshotSource(ctx, project.ID, issue.ID, sourcePath)
}

func (e *Engine) prepareGitSource(ctx context.Context, projectID, issueID int64, cfg gorchgit.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	mgr := &gorchgit.Manager{StorageRoot: e.cfg.StorageRoot}
	if err := mgr.EnsureCache(ctx, projectID, cfg); err != nil {
		return err
	}
	abs := storage.Abs(e.cfg.StorageRoot, storage.SourcePath(projectID, issueID))
	return mgr.CreateSourceWorktree(ctx, projectID, abs, cfg)
}

// prepareImplementerWorkspace creates a git worktree branch or seeds from snapshot.
func (e *Engine) prepareImplementerWorkspace(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, run *sqlite.Run) error {
	gitCfg, err := e.projectGitConfig(project)
	if err != nil {
		return err
	}
	wsKey := storage.WorkspacePath(project.ID, issue.ID)
	if gitCfg.Enabled() {
		mgr := &gorchgit.Manager{StorageRoot: e.cfg.StorageRoot}
		if err := mgr.EnsureCache(ctx, project.ID, gitCfg); err != nil {
			return err
		}
		branch := gorchgit.BranchName(issue.ID, run.ID)
		abs := storage.Abs(e.cfg.StorageRoot, wsKey)
		if err := mgr.CreateImplementerWorktree(ctx, project.ID, abs, branch, gitCfg); err != nil {
			return err
		}
		return e.runs.SetWorkspace(run.ID, wsKey, branch)
	}
	sourceSnapshotPath := storage.SourcePath(project.ID, issue.ID)
	if err := e.seedWorkspace(ctx, project.ID, issue.ID, sourceSnapshotPath); err != nil {
		return err
	}
	return e.runs.SetWorkspace(run.ID, wsKey, "")
}

// commitImplementerWorkspace stages and commits (and optionally pushes/PRs).
func (e *Engine) commitImplementerWorkspace(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, run *sqlite.Run) error {
	gitCfg, err := e.projectGitConfig(project)
	if err != nil {
		return err
	}
	if !gitCfg.Enabled() {
		return nil
	}
	wsKey := storage.WorkspacePath(project.ID, issue.ID)
	abs := storage.Abs(e.cfg.StorageRoot, wsKey)
	mgr := &gorchgit.Manager{StorageRoot: e.cfg.StorageRoot}
	msg := gorchgit.CommitMessage(issue.Title, issue.ID, run.ID)
	created, err := mgr.CommitAll(ctx, abs, msg, gitCfg.AuthorName, gitCfg.AuthorEmail)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	branch := run.BranchName
	if branch == "" {
		branch = gorchgit.BranchName(issue.ID, run.ID)
	}
	if gitCfg.Push {
		if err := mgr.Push(ctx, abs, branch); err != nil {
			return err
		}
	}
	if gitCfg.CreatePR {
		body := fmt.Sprintf("Automated PR for issue #%d (run %d).", issue.ID, run.ID)
		if err := mgr.CreatePR(ctx, abs, gitCfg.BaseBranch, issue.Title, body); err != nil {
			return err
		}
	}
	return nil
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

// typedProjectConfig prefers live YAML (cfg.Projects); falls back to config_json
// for orphan historical projects no longer in YAML.
func (e *Engine) typedProjectConfig(project *sqlite.Project) (config.ProjectConfig, error) {
	if e.cfg.Projects != nil {
		if pc, ok := e.cfg.Projects[project.Name]; ok {
			return pc, nil
		}
	}
	var pc config.ProjectConfig
	if project.ConfigJSON == "" || project.ConfigJSON == "{}" {
		return pc, nil
	}
	if err := json.Unmarshal([]byte(project.ConfigJSON), &pc); err != nil {
		return pc, fmt.Errorf("parse project config: %w", err)
	}
	return pc, nil
}

// projectSourcePath extracts the source path from project config.
func (e *Engine) projectSourcePath(project *sqlite.Project) (string, error) {
	pc, err := e.typedProjectConfig(project)
	if err != nil {
		return "", err
	}
	return pc.SourcePath, nil
}

// projectGitConfig extracts git config from project config.
func (e *Engine) projectGitConfig(project *sqlite.Project) (gorchgit.Config, error) {
	pc, err := e.typedProjectConfig(project)
	if err != nil {
		return gorchgit.Config{}, err
	}
	if pc.Git == nil || strings.TrimSpace(pc.Git.RepoURL) == "" {
		return gorchgit.Config{}, nil
	}
	return gorchgit.Config{
		RepoURL:     pc.Git.RepoURL,
		BaseBranch:  pc.Git.BaseBranch,
		Push:        pc.Git.Push,
		CreatePR:    pc.Git.CreatePR,
		AuthorName:  pc.Git.AuthorName,
		AuthorEmail: pc.Git.AuthorEmail,
		Auth: gorchgit.AuthConfig{
			Type:       gorchgit.AuthType(pc.Git.Auth.Type),
			SSHKeyPath: pc.Git.Auth.SSHKeyPath,
			TokenEnv:   pc.Git.Auth.TokenEnv,
			GHProfile:  pc.Git.Auth.GHProfile,
		},
	}, nil
}

// projectTestConfig extracts the immutable test command block.
func (e *Engine) projectTestConfig(project *sqlite.Project) (tools.TestConfig, error) {
	pc, err := e.typedProjectConfig(project)
	if err != nil {
		return tools.TestConfig{}, err
	}
	if pc.Test == nil || pc.Test.Command == "" {
		return tools.TestConfig{}, nil
	}
	out := tools.TestConfig{
		Command:    pc.Test.Command,
		Image:      pc.Test.Image,
		CPU:        pc.Test.CPU,
		Memory:     pc.Test.Memory,
		SecretsEnv: pc.Test.SecretsEnv,
		Runtime:    pc.Test.Runtime,
	}
	if pc.Test.Timeout != "" {
		d, err := time.ParseDuration(pc.Test.Timeout)
		if err != nil {
			return tools.TestConfig{}, fmt.Errorf("parse test.timeout: %w", err)
		}
		out.Timeout = d
	}
	return out, nil
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

// mapPhaseResultToIssueStatus converts a phase result.json status into an
// issue-row status. Phase "done" must not mark the issue done — the pipeline
// may still have plan/implementation left. Only runPipeline's final UpdateStatus
// sets issue status to done after all phases complete.
func mapPhaseResultToIssueStatus(phaseResult string) string {
	switch phaseResult {
	case "done", "retry", "in_progress":
		return sqlite.StatusInProgress
	case "waiting_human":
		return sqlite.StatusWaitingHuman
	case "failed":
		return sqlite.StatusFailed
	case "cancelled":
		return sqlite.StatusCancelled
	default:
		if phaseResult == "" {
			return sqlite.StatusInProgress
		}
		return phaseResult
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

// nextPhaseName returns the following pipeline phase, or "" after implementation.
func nextPhaseName(phase string) string {
	switch phase {
	case "research":
		return "plan"
	case "plan":
		return "implementation"
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
			"parameters":  llm.DeclarationParameters(decl),
		})
	}
	return out, nil
}
