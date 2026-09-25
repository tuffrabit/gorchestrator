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
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

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
	// AgentType keeps the historical task.json key; it now holds the step's
	// agent id (a user-chosen id under agents:).
	AgentType         string            `json:"agent_type"`
	StepKey           string            `json:"step_key"`
	Flow              []string          `json:"flow"`
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
	Type       string         `json:"type"`
	Timestamp  string         `json:"timestamp"`
	Attempt    int            `json:"attempt"`
	Loop       int            `json:"loop"`
	Role       string         `json:"role,omitempty"`
	Content    string         `json:"content,omitempty"`
	ToolCall   map[string]any `json:"tool_call,omitempty"`
	ToolResult map[string]any `json:"tool_result,omitempty"`
	Tokens     int            `json:"tokens,omitempty"`
	Error      string         `json:"error,omitempty"`
	// Model and DurationMs are set on model_wait/model_load/model_reuse/
	// model_unload/model_sideload residency events.
	Model      string `json:"model,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// Engine executes the multi-agent pipeline.
type Engine struct {
	cfg        *config.Config
	store      storage.Port
	db         *sql.DB
	projects   *sqlite.ProjectRepo
	issues     *sqlite.IssueRepo
	runs       *sqlite.RunRepo
	decisions  *sqlite.DecisionRepo
	users      *sqlite.UserRepo
	sessions   *sqlite.SessionRepo
	audit      *sqlite.AuditRepo
	notifs     *sqlite.NotificationRepo
	chatRepo   *sqlite.ChatRepo
	bus        *EventBus
	notifier   *notify.Dispatcher
	escalator  *notify.Escalator
	mcp        *gorchmcp.Manager
	controller Controller
	breaker    *inferenceBreaker
	// chatSvc runs dashboard chat conversations (web UI chat drawer).
	chatSvc *ChatService
	// modelActMu guards modelActivity: per-issue in-flight inference
	// lifecycle work (wait/load/unload), surfaced on the dashboard.
	modelActMu    sync.Mutex
	modelActivity map[int64]ModelActivity
	// runMu guards runRegs: per-issue in-flight pipeline runs, keyed by
	// issue ID. StopIssue cancels the registered run's context.
	runMu   sync.Mutex
	runRegs map[int64]*runReg
}

// runReg tracks one in-flight pipeline run for an issue. stopped records
// that cancellation came from a user stop (vs daemon shutdown) so
// runPipeline can land the issue on StatusStopped instead of cancelled.
type runReg struct {
	cancel  context.CancelFunc
	stopped bool
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
		chatRepo:  sqlite.NewChatRepo(db),
		bus:       NewEventBus(),
		breaker:   &inferenceBreaker{},
		runRegs:   map[int64]*runReg{},
	}
	e.chatSvc = newChatService(e)
	if cfg.Inference.Type != "" {
		e.controller = NewController(cfg.Inference)
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

// ListRegisteredProjects returns synced project rows for names present in
// YAML, each paired with the configured agent ids and the project's
// default_flow for the submit UI.
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
			Project:         p,
			AvailableAgents: e.cfg.AgentIDs(),
			DefaultFlow:     pc.DefaultFlow,
		})
	}
	return out, nil
}

// RegisteredProject is a YAML-registered project with the submit-UI agent data.
type RegisteredProject struct {
	Project         *sqlite.Project
	AvailableAgents []string
	DefaultFlow     []string
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

// SetEscalator attaches the YAML escalation evaluator (may be nil).
func (e *Engine) SetEscalator(esc *notify.Escalator) {
	e.escalator = esc
}

// Escalator returns the escalation evaluator (may be nil).
func (e *Engine) Escalator() *notify.Escalator {
	return e.escalator
}

// SetMCP attaches an MCP manager (per-agent server allowlists).
func (e *Engine) SetMCP(m *gorchmcp.Manager) {
	e.mcp = m
}

// MCP returns the MCP manager (may be nil).
func (e *Engine) MCP() *gorchmcp.Manager {
	return e.mcp
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
	// Flow is the ordered list of agent ids for this issue. When empty it is
	// resolved from projects.<name>.default_flow; a submit with neither is
	// rejected (resolveFlow).
	Flow []string
	// DependsOn lists issue IDs that must reach done before this issue is
	// claimable by daemon workers. Every ID must reference an existing issue.
	DependsOn []int64
}

// Run creates a new issue and executes the full pipeline.
func (e *Engine) Run(ctx context.Context, opts RunOptions) error {
	project, err := e.resolveRegisteredProject(opts.ProjectName)
	if err != nil {
		return err
	}

	flow, err := e.resolveFlow(project.Name, opts.Flow)
	if err != nil {
		return err
	}
	pipelineJSON, err := marshalFlowJSON(flow)
	if err != nil {
		return err
	}

	dependsOnJSON, err := e.validateDependsOn(opts.DependsOn)
	if err != nil {
		return err
	}

	issue, err := e.issues.CreateWithStatus(project.ID, opts.IssueTitle, sqlite.StatusInProgress, "step-1", pipelineJSON, "{}", dependsOnJSON, false, "manual", "")
	if err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	if err := e.persistIssueContext(ctx, issue, opts.IssueTitle, opts.Description, opts.Attachments); err != nil {
		return fmt.Errorf("persist issue context: %w", err)
	}

	if err := e.prepareIssueSource(ctx, project, issue); err != nil {
		return fmt.Errorf("prepare source: %w", err)
	}

	if err := e.maybeHoldForScope(ctx, project, issue, opts); err != nil {
		return err
	}
	issue, err = e.issues.Get(issue.ID)
	if err != nil || issue == nil {
		return fmt.Errorf("reload issue after scope check: %w", err)
	}
	if issue.Status == sqlite.StatusWaitingHuman {
		// Scope hold — do not run the pipeline; caller uses resume/decide.
		return nil
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

	steps, err := e.stepsForIssue(issue)
	if err != nil {
		return err
	}
	phase, status, err := e.currentStepState(project.ID, issue.ID, steps)
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

// validateDependsOn verifies that every dependency references an existing issue
// and marshals the deduplicated list for storage. Deps can only point at issues
// that already exist at submit time, so cycles are impossible by construction.
func (e *Engine) validateDependsOn(deps []int64) (string, error) {
	if len(deps) == 0 {
		return "[]", nil
	}
	seen := make(map[int64]bool, len(deps))
	ids := make([]int64, 0, len(deps))
	for _, id := range deps {
		if id <= 0 {
			return "", fmt.Errorf("depends_on issue id %d is invalid", id)
		}
		if seen[id] {
			continue
		}
		dep, err := e.issues.Get(id)
		if err != nil {
			return "", fmt.Errorf("depends_on lookup issue %d: %w", id, err)
		}
		if dep == nil {
			return "", fmt.Errorf("depends_on issue %d does not exist", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("marshal depends_on: %w", err)
	}
	return string(data), nil
}

// runPipeline walks the issue's frozen agent flow step by step.
func (e *Engine) runPipeline(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, dryRun bool) error {
	steps, err := e.stepsForIssue(issue)
	if err != nil {
		return err
	}

	for _, step := range steps {
		phaseName := step.Key
		currentPhase, status, err := e.currentStepState(project.ID, issue.ID, steps)
		if err != nil {
			return err
		}

		// currentStepState returns the first non-done step. If it reports a
		// step ahead of the one we expect, that earlier step is already done.
		stepIdx := stepIndex(steps, phaseName)
		currentIdx := stepIndex(steps, currentPhase)
		if currentIdx > stepIdx {
			continue
		}

		// If currentStepState points at an earlier step than our loop variable,
		// use the current step so we pick up the correct config and input.
		if currentIdx < stepIdx {
			step = steps[currentIdx]
			phaseName = step.Key
		}
		phaseCfg, err := e.stepAgentConfig(issue, step)
		if err != nil {
			e.failIssuePhaseError(ctx, project, issue, phaseName, err)
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

		// Drive inference-server residency for this phase (no-op without an
		// inference config). The model stays resident across adjudication
		// retries inside runPhase; release frees the exclusive-mode lock once
		// the phase's result.json is persisted, keeping the model loaded when
		// the pipeline will immediately continue.
		releaseModel, err := e.acquirePhaseModel(ctx, project, issue, phaseName, phaseCfg)
		if err != nil {
			e.failIssueInferenceError(ctx, project, issue, phaseName, err)
			return fmt.Errorf("load model for %s: %w", phaseName, err)
		}

		// Issue status is independent of phase result.json status. Mark the
		// issue in_progress on the new phase *before* work starts so SSE/UI
		// can show the transition (research → plan → implementation).
		_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusInProgress, phaseName)
		e.Publish(Event{
			Type: EventPhaseStarted, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phaseName, Status: sqlite.StatusInProgress,
		})

		prevKey := prevStepKey(steps, phaseName)
		prevAgent := ""
		if pi := stepIndex(steps, prevKey); pi >= 0 {
			prevAgent = steps[pi].AgentID
		}
		baseInput, err := e.buildBaseInput(ctx, project.ID, issue.ID, phaseName, prevKey, prevAgent, issue.Title, issue.Description)
		if err != nil {
			e.failIssuePhaseError(ctx, project, issue, phaseName, err)
			return fmt.Errorf("build input for %s: %w", phaseName, err)
		}

		// Single-shot phases get no tools, so pre-stuff repo context into the
		// prompt. The digest is constant per issue (the source snapshot is
		// frozen at issue creation) and survives retry attempts via baseInput.
		if phaseCfg.SingleShot != nil && *phaseCfg.SingleShot {
			digest, err := e.buildRepoDigest(ctx, project.ID, issue.ID, phaseCfg)
			if err != nil {
				e.failIssuePhaseError(ctx, project, issue, phaseName, err)
				return fmt.Errorf("build repo digest for %s: %w", phaseName, err)
			}
			if digest != "" {
				baseInput += "\n\n" + digest
			}
		}

		result, err := e.runPhase(ctx, project, issue, phaseName, phaseCfg, baseInput, dryRun)
		if err != nil {
			// Hard error: result.json may not be persisted. Still release the
			// model/lock, but don't let an unload failure mask the real error.
			_ = releaseModel(false)
			if e.stopRequested(issue.ID) {
				// User-requested stop: not an error — land on stopped.
				_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusStopped, phaseName)
				e.Publish(Event{
					Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
					Phase: phaseName, Status: sqlite.StatusStopped, Message: "stopped by user",
				})
				return nil
			}
			e.failIssuePhaseError(ctx, project, issue, phaseName, err)
			return fmt.Errorf("run phase %s: %w", phaseName, err)
		}

		// The phase's result.json is durably written at this point. Unload on
		// terminal outcomes (waiting_human, failed, stopped, pipeline end) — a
		// phase waiting on a human must not pin the model. When the pipeline
		// continues immediately, keep the model resident: the next phase's
		// acquire reuses it without an unload/reload cycle (a no-op outside
		// exclusive mode). Fail safe on unload failure: residency is unknown,
		// so return before the next phase starts. The persisted result lets
		// resume/recovery pick the phase up correctly.
		keep := result.Status == "done" && nextStepKey(steps, phaseName) != ""
		if keep {
			// Keeping a resident model only pays off when the next step wants
			// the same one; otherwise release so the next acquire doesn't have
			// to unload a foreign model first.
			if ni := stepIndex(steps, nextStepKey(steps, phaseName)); ni >= 0 {
				if nextCfg, cerr := e.stepAgentConfig(issue, steps[ni]); cerr != nil || nextCfg.Model.Model != phaseCfg.Model.Model {
					keep = false
				}
			}
		}
		if err := releaseModel(keep); err != nil {
			e.failIssueInferenceError(ctx, project, issue, phaseName, err)
			return fmt.Errorf("unload model after %s: %w", phaseName, err)
		}

		issueStatus := mapPhaseResultToIssueStatus(result.Status)
		if issueStatus == sqlite.StatusCancelled && e.stopRequested(issue.ID) {
			issueStatus = sqlite.StatusStopped
		}
		// On phase success, point current_phase at the *next* stage immediately so
		// the dashboard shows the transition before the next phase_started lands.
		phaseForIssue := phaseName
		if result.Status == "done" {
			if next := nextStepKey(steps, phaseName); next != "" {
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
			if e.escalator != nil {
				e.escalator.Observe(ctx, notify.Event{Success: true, Project: project.Name, IssueID: issue.ID, Phase: phaseName})
			}
			// Pipeline continues; issue stays in_progress until all steps finish.
			continue
		case "waiting_human":
			e.Publish(Event{
				Type: EventDecisionRequested, IssueID: issue.ID, ProjectID: project.ID,
				Phase: phaseName, Status: sqlite.StatusWaitingHuman,
			})
			notify.NotifyHumanGate(ctx, e.notifier, issue.ID, phaseName, project.Name, issue.Title, e.adminEmails())
			return nil
		default:
			if issueStatus == sqlite.StatusStopped {
				// User-requested stop: not bad output — no notifications or
				// escalation, and no error for the worker to log.
				return nil
			}
			notify.NotifyBadOutput(ctx, e.notifier, issue.ID, phaseName, result.Error, e.adminEmails())
			e.observeEscalation(ctx, project.Name, issue.ID, phaseName, result.Error)
			return fmt.Errorf("phase %s %s: %s", phaseName, result.Status, result.Error)
		}
	}

	lastKey := steps[len(steps)-1].Key
	_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusDone, lastKey)
	e.Publish(Event{
		Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
		Phase: lastKey, Status: sqlite.StatusDone,
	})
	return nil
}

// acquirePhaseModel drives inference-server residency for one pipeline phase
// when an inference block is configured. A phase whose agent config sets
// sideload exits immediately with a no-op release: the model lives outside the
// swap lifecycle (no lock, no explicit load — llama-swap serves it on request —
// and no unload). In exclusive mode it takes the server-keyed lock, then
// reconciles residency, ignoring sideloaded models: if exactly the phase's
// model is resident it is reused as-is (no unload/reload cycle); if a foreign
// non-sideloaded model is resident it is unloaded first; otherwise the model
// is loaded. It returns a release func — release(true) keeps the model
// resident (exclusive mode only) so the next acquirer can reuse it, and just
// frees the lock; release(false) unloads all non-sideloaded models and frees
// the lock. The release func always releases the lock, including when unload
// fails. With no inference config both steps are no-ops and the server swaps
// implicitly per request.
func (e *Engine) acquirePhaseModel(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phaseName string, phaseCfg config.AgentConfig) (func(keepResident bool) error, error) {
	inf := e.cfg.Inference
	if inf.Type == "" || e.controller == nil {
		return func(bool) error { return nil }, nil
	}
	model := phaseCfg.Model.Model
	eventsPath := storage.EventsPath(project.ID, issue.ID, phaseName)
	record := func(ev eventRecord) {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
		recordEvent(ctx, e.store, eventsPath, ev)
	}

	// Sideloaded models coexist with the swapped main model: this phase takes
	// no lock and drives no load/unload, and other phases spare the model from
	// their evictions (the keep list below).
	if phaseCfg.Sideload != nil && *phaseCfg.Sideload {
		record(eventRecord{Type: "model_sideload", Model: model})
		return func(bool) error { return nil }, nil
	}
	sideloaded := e.sideloadedModels()
	keep := make([]string, 0, len(sideloaded))
	for m := range sideloaded {
		keep = append(keep, m)
	}
	// publishActivity mirrors lifecycle transitions to the dashboard: a nil
	// act clears the chip, otherwise the card re-renders showing it.
	publishActivity := func(act *ModelActivity) {
		msg := ""
		if act != nil {
			e.setModelActivity(issue.ID, *act)
			msg = act.Label()
		} else {
			e.clearModelActivity(issue.ID)
		}
		e.Publish(Event{
			Type: EventModelActivity, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phaseName, Message: msg,
		})
	}

	locked := false
	// release frees the exclusive-mode lock. keepResident (exclusive mode only)
	// skips the unload so the next acquirer — this issue's next phase or another
	// issue — can reuse the resident model; acquire reconciles either way.
	release := func(keepResident bool) error {
		if keepResident && locked {
			modelLocks.Unlock(inf.BaseURL)
			return nil
		}
		publishActivity(&ModelActivity{Op: ModelOpUnload, Model: model, Phase: phaseName})
		defer publishActivity(nil)
		start := time.Now()
		// Unload with a fresh context: the run ctx may already be cancelled
		// (user stop / daemon shutdown), and the model must still be
		// unloaded so exclusive mode does not wedge on a resident model.
		unloadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := e.controller.UnloadAll(unloadCtx, keep...)
		ev := eventRecord{Type: "model_unload", DurationMs: time.Since(start).Milliseconds()}
		if err != nil {
			ev.Error = err.Error()
			log.Printf("issue %d phase %s: model unload failed: %v", issue.ID, phaseName, err)
		}
		record(ev)
		if locked {
			modelLocks.Unlock(inf.BaseURL)
		}
		return err
	}

	if inf.Mode == "exclusive" {
		key := inf.BaseURL
		if !modelLocks.TryLock(key) {
			record(eventRecord{Type: "model_wait", Model: model})
			publishActivity(&ModelActivity{Op: ModelOpWait, Model: model, Phase: phaseName})
			modelLocks.Lock(key)
			publishActivity(nil)
		}
		locked = true

		// Reconcile residency before loading: a previous phase (or issue) may
		// have kept its model resident. Reusing it skips the unload/reload
		// cycle entirely; a foreign model must be evicted first so the phase
		// never runs against the wrong model. Sideloaded models coexist with
		// any other model, so they are invisible to this decision.
		running, err := e.controller.RunningModels(ctx)
		if err != nil {
			runningErr := fmt.Errorf("check running models: %w", err)
			record(eventRecord{Type: "model_load", Model: model, Error: runningErr.Error()})
			modelLocks.Unlock(inf.BaseURL)
			return nil, runningErr
		}
		resident := make([]string, 0, len(running))
		for _, m := range running {
			if !sideloaded[m] {
				resident = append(resident, m)
			}
		}
		switch {
		case len(resident) == 1 && resident[0] == model:
			record(eventRecord{Type: "model_reuse", Model: model})
			return release, nil
		case len(resident) > 0:
			publishActivity(&ModelActivity{Op: ModelOpUnload, Model: resident[0], Phase: phaseName})
			start := time.Now()
			unloadErr := e.controller.UnloadAll(ctx, keep...)
			publishActivity(nil)
			unloadEv := eventRecord{Type: "model_unload", DurationMs: time.Since(start).Milliseconds()}
			if unloadErr != nil {
				unloadEv.Error = unloadErr.Error()
				record(unloadEv)
				modelLocks.Unlock(inf.BaseURL)
				return nil, unloadErr
			}
			record(unloadEv)
		}
	}

	publishActivity(&ModelActivity{Op: ModelOpLoad, Model: model, Phase: phaseName})
	start := time.Now()
	loadErr := e.controller.EnsureLoaded(ctx, model, modelTimeout(phaseCfg.Model))
	publishActivity(nil)
	loadEv := eventRecord{Type: "model_load", Model: model, DurationMs: time.Since(start).Milliseconds()}
	if loadErr != nil {
		loadEv.Error = loadErr.Error()
		record(loadEv)
		if locked {
			modelLocks.Unlock(inf.BaseURL)
		}
		return nil, loadErr
	}
	record(loadEv)

	return release, nil
}

// sideloadedModels returns the set of model names configured with
// sideload: true across the global agents (YAML). There are no project
// overlays anymore: sideloading is a per-agent property.
func (e *Engine) sideloadedModels() map[string]bool {
	out := map[string]bool{}
	for _, cfg := range e.cfg.Agents {
		if cfg.Sideload != nil && *cfg.Sideload && cfg.Model.Model != "" {
			out[cfg.Model.Model] = true
		}
	}
	return out
}

// failIssueInferenceError marks an issue failed after an inference-side error
// (lifecycle controller load/unload), making the failure visible: issue row,
// phase_finished SSE event, an inference_error entry in the phase's
// events.jsonl, and a bad-output notification. In exclusive mode it also
// trips the inference breaker so daemon workers stop claiming new issues
// until a human decides on this one. Agent output quality and LLM-call
// failures inside runPhase deliberately do not take this path.
func (e *Engine) failIssueInferenceError(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phaseName string, cause error) {
	msg := fmt.Sprintf("inference error during %s: %v", phaseName, cause)
	msg = withDiagnosis(diagnoseFailure(ctx, e.store, storage.EventsPath(project.ID, issue.ID, phaseName), cause), msg)
	_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusFailed, phaseName)
	e.Publish(Event{
		Type: EventPhaseFinished, IssueID: issue.ID, ProjectID: project.ID,
		Phase: phaseName, Status: sqlite.StatusFailed, Message: msg,
		Data: map[string]any{
			"phase_result":  "inference_error",
			"current_phase": phaseName,
		},
	})
	recordEvent(ctx, e.store, storage.EventsPath(project.ID, issue.ID, phaseName), eventRecord{
		Type:      "inference_error",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Error:     msg,
	})
	notify.NotifyBadOutput(ctx, e.notifier, issue.ID, phaseName, msg, e.adminEmails())
	if e.cfg.Inference.Mode == "exclusive" && e.breaker.Trip(issue.ID, msg) {
		e.Publish(Event{
			Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phaseName, Status: sqlite.StatusFailed,
			Message: "inference breaker tripped: " + msg,
		})
	}
}

// failIssuePhaseError marks an issue failed when a phase dies before a
// result.json exists (config resolution, input/digest build, workspace
// preparation). Without it the issue row stays in_progress forever: the
// daemon worker logs the error and moves on, leaving the UI stuck "active".
func (e *Engine) failIssuePhaseError(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phaseName string, cause error) {
	msg := fmt.Sprintf("phase %s error: %v", phaseName, cause)
	msg = withDiagnosis(diagnoseFailure(ctx, e.store, storage.EventsPath(project.ID, issue.ID, phaseName), cause), msg)
	_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusFailed, phaseName)
	e.Publish(Event{
		Type: EventPhaseFinished, IssueID: issue.ID, ProjectID: project.ID,
		Phase: phaseName, Status: sqlite.StatusFailed, Message: msg,
		Data: map[string]any{
			"phase_result":  "error",
			"current_phase": phaseName,
		},
	})
	recordEvent(ctx, e.store, storage.EventsPath(project.ID, issue.ID, phaseName), eventRecord{
		Type:      "phase_error",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Error:     msg,
	})
	notify.NotifyBadOutput(ctx, e.notifier, issue.ID, phaseName, msg, e.adminEmails())
}

// runPhase runs a single phase with adjudication attempts.
func (e *Engine) runPhase(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phase string, cfg config.AgentConfig, baseInput string, dryRun bool) (*PhaseResult, error) {
	steps, err := e.stepsForIssue(issue)
	if err != nil {
		return nil, fmt.Errorf("resolve steps: %w", err)
	}
	prevKey := prevStepKey(steps, phase)
	flow := flowAgentIDs(steps)

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
		} else if fb := e.humanGateFeedback(ctx, project.ID, issue.ID, phase); fb != "" {
			// Decide on a cleared scope hold leaves feedback at attempt 1
			// with no result.json; the held step never ran, so there is no
			// rejected output — inject the feedback alone into the first run.
			input += "\n\nHuman gate feedback:\n" + fb
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
		task, err := e.buildTask(ctx, project.ID, issue.ID, phase, cfg, flow, prevKey, attempt, outputPath, dryRun)
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

		// Create run record for per-attempt usage tracking.
		modelName := cfg.Model.Model
		if dryRun {
			modelName = "dryrun"
		}
		run, err := e.runs.Create(issue.ID, cfg.ID, modelName, "in_progress")
		if err != nil {
			return nil, fmt.Errorf("create run: %w", err)
		}

		// Steps whose agent can edit files work on the shared issue-level
		// workspace (git worktree or snapshot copy), prepared once per issue.
		if cfg.HasEditingTool() {
			if err := e.prepareIssueWorkspaceOnce(ctx, project, issue); err != nil {
				return nil, fmt.Errorf("prepare workspace: %w", err)
			}
			wsKey, werr := e.workspaceKey(ctx, issue)
			if werr != nil {
				return nil, fmt.Errorf("resolve workspace: %w", werr)
			}
			branchName := ""
			if gitCfg, gerr := e.projectGitConfig(project); gerr == nil && gitCfg.Enabled() {
				branchName = gorchgit.IssueBranchName(issue.ID)
			}
			if err := e.runs.SetWorkspace(run.ID, wsKey, branchName); err != nil {
				return nil, fmt.Errorf("set run workspace: %w", err)
			}
		}

		// Every step may read the workspace: the workspace is issue-level and a
		// non-editing step (e.g. a reviewer narrowed to read-only tools) still
		// needs to see what an earlier step wrote. Which steps can MUTATE it is
		// governed by tools:, not by this allowlist — the allowlist is path
		// scoping, not a capability boundary.
		allowlist := []string{
			storage.IssueDir(project.ID, issue.ID),
			storage.SourcePath(project.ID, issue.ID),
			storage.WorkspacePath(project.ID, issue.ID),
		}

		// Agents need explicit path guidance; short names like "source" only
		// work once BasePath/allowlist resolution is documented in context.
		input = input + pathGuide(project.ID, issue.ID, phase, cfg.HasEditingTool(), allowlist)

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
				errMsg = withDiagnosis(diagnoseFailure(ctx, e.store, eventsPath, loopErr), loopErr.Error())
			}
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
			// After an accepted step whose agent can edit files, commit the
			// issue workspace so each accepted step lands in git history in
			// order (no-op when nothing changed or the project is not
			// git-backed).
			if cfg.HasEditingTool() {
				if err := e.commitIssueWorkspace(ctx, project, issue, phase); err != nil {
					log.Printf("git commit after %s: %v", phase, err)
				}
			}
			result.Status = "done"
			result.DoneRationale = doneRationale
			if err := writeResult(ctx, e.store, resultPath, *result); err != nil {
				return nil, err
			}
			return result, nil
		case adjudication.Fail:
			result.Status = "failed"
			result.Error = withDiagnosis(diagnoseFailure(ctx, e.store, eventsPath, nil), decision.Feedback)
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
			result.Error = withDiagnosis(diagnoseFailure(ctx, e.store, eventsPath, nil), "max attempts exceeded: "+decision.Feedback)
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

	return &PhaseResult{Status: "failed", Error: withDiagnosis(diagnoseFailure(ctx, e.store, storage.EventsPath(project.ID, issue.ID, phase), nil), "max attempts exceeded")}, nil
}

// buildPhaseModel constructs the phase LLM from the merged agent config and
// wraps it with the provider session budget (rehydrated from events.jsonl).
func (e *Engine) buildPhaseModel(ctx context.Context, cfg config.AgentConfig, issueID int64, phase, eventsPath string, dryRun bool) (adkmodel.LLM, error) {
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
		return nil, fmt.Errorf("build model: %w", err)
	}
	// Provider session budget: per-phase ceiling, rehydrate from events.jsonl.
	providerName := modelCfg.Provider
	var issueRow *sqlite.Issue
	if issueID > 0 {
		issueRow, _ = e.issues.Get(issueID)
	}
	var projectRow *sqlite.Project
	if issueRow != nil {
		projectRow, _ = e.projects.Get(issueRow.ProjectID)
	}
	return e.wrapModelWithBudget(ctx, issueRow, projectRow, phase, providerName, eventsPath, dryRun, llmModel), nil
}

// runAgentLoop runs one loop of an agent and returns the loop output, the
// finish_task done flag, rationale, token count, and any error.
func (e *Engine) runAgentLoop(ctx context.Context, projectID, issueID int64, phase string, cfg config.AgentConfig, userContent *genai.Content, outputPath, eventsPath string, allowlist []string, attempt, loop int, runID int64, dryRun bool) ([]byte, bool, string, int, error) {
	// Single-shot agents skip tools/MCP and the ADK runner entirely.
	if cfg.SingleShot != nil && *cfg.SingleShot {
		llmModel, err := e.buildPhaseModel(ctx, cfg, issueID, phase, eventsPath, dryRun)
		if err != nil {
			return nil, false, "", 0, err
		}
		return e.runSingleShot(ctx, llmModel, cfg, userContent, outputPath, eventsPath, attempt, loop)
	}

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
	// Every step's short paths resolve against the shared issue-level
	// workspace (a non-editing step may still need to read what an earlier
	// step wrote; mutation is controlled by tools:, not by this binding).
	issue, issueErr := e.issues.Get(issueID)
	if issueErr == nil && issue != nil {
		if wsKey, werr := e.workspaceKey(ctx, issue); werr == nil {
			bt.WorkspacePath = wsKey
			bt.WorkspaceHostPath = storage.Abs(e.cfg.StorageRoot, wsKey)
			bt.BasePath = wsKey
		}
	}
	// Resolve project for test config (issue → project).
	if issue != nil {
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

	registry, err := tools.NewCoreRegistry(bt)
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

	llmModel, err := e.buildPhaseModel(ctx, cfg, issueID, phase, eventsPath, dryRun)
	if err != nil {
		return nil, false, "", 0, err
	}

	agentInst, err := e.buildAgent(cfg, llmModel, registry)
	if err != nil {
		return nil, false, "", 0, fmt.Errorf("build agent: %w", err)
	}

	wrapper, err := agent.New(agent.Config{
		Name:        cfg.ID + "-runner",
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
			// Record the terminal error so the activity log ends with an
			// explanation instead of dangling tool calls.
			recordEvent(ctx, e.store, eventsPath, eventRecord{
				Type:      "loop_error",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Attempt:   attempt,
				Loop:      loop,
				Error:     err.Error(),
			})
			if llm.IsBudgetExceeded(err) {
				return nil, false, "", loopTokens, err
			}
			return nil, false, "", 0, fmt.Errorf("loop %d: %w", loop, err)
		}
		if ev == nil || ev.Content == nil {
			continue
		}

		if ev.UsageMetadata != nil {
			callTokens := int(ev.UsageMetadata.TotalTokenCount)
			if callTokens <= 0 {
				callTokens = int(ev.UsageMetadata.PromptTokenCount + ev.UsageMetadata.CandidatesTokenCount)
			}
			if callTokens > 0 {
				loopTokens += callTokens
				recordEvent(ctx, e.store, eventsPath, eventRecord{
					Type:      "usage",
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Attempt:   attempt,
					Loop:      loop,
					Tokens:    callTokens,
				})
			}
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

	// Per-call usage events are written above; no loop-aggregate row (would double-count rehydrate).

	// If the agent did not use write_output, fall back to the final model text.
	if !outputWritten && finalText != "" {
		if err := e.store.Write(ctx, outputPath, []byte(finalText)); err != nil {
			return nil, false, "", 0, fmt.Errorf("write fallback output: %w", err)
		}
	}

	// For editing agents the real output is the workspace; create a summary
	// output.md if the agent did not write one.
	if cfg.HasEditingTool() {
		if exists, _ := e.store.Exists(ctx, outputPath); !exists {
			summary := fmt.Sprintf("%s complete.\n\nRationale: %s", cfg.ID, finishRationale)
			if err := e.store.Write(ctx, outputPath, []byte(summary)); err != nil {
				return nil, false, "", 0, fmt.Errorf("write workspace summary: %w", err)
			}
		}
	}

	output, _ := e.store.Read(ctx, outputPath)
	if !cfg.HasEditingTool() && !outputWritten && finalText == "" && len(output) == 0 {
		return nil, false, "", 0, fmt.Errorf("loop %d produced empty output", loop)
	}

	if finishDone == nil {
		return nil, false, "", 0, fmt.Errorf("loop %d did not call finish_task", loop)
	}

	return output, *finishDone, finishRationale, loopTokens, nil
}

// buildAgent constructs the ADK agent for one step of the issue flow.
// Config validation guarantees a system prompt; an empty one here means the
// config was built in-process (tests) — fail loudly rather than run an
// instruction-less agent.
func (e *Engine) buildAgent(cfg config.AgentConfig, llmModel adkmodel.LLM, tools []tool.Tool) (agent.Agent, error) {
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		return nil, fmt.Errorf("agent %q has no system_prompt", cfg.ID)
	}
	return agents.NewTask(cfg.ID, cfg.SystemPrompt).Build(llmModel, tools)
}

// buildTask constructs the task.json content for a step. prevKey is the key
// of the preceding step ("" for the first); its accepted output is listed in
// input_context_paths.
func (e *Engine) buildTask(ctx context.Context, projectID, issueID int64, phase string, cfg config.AgentConfig, flow []string, prevKey string, attempt int, outputPath string, dryRun bool) (PhaseTask, error) {
	model := map[string]string{
		"provider": cfg.Model.Provider,
		"model":    cfg.Model.Model,
	}
	if dryRun {
		model["provider"] = "dryrun"
		model["model"] = "dryrun"
	}

	toolList, err := tools.NewCoreRegistry(&tools.BoundTools{})
	if err != nil {
		return PhaseTask{}, err
	}
	toolList = tools.FilterByNames(toolList, cfg.Tools)
	toolSchemas, err := schemasFromTools(toolList)
	if err != nil {
		return PhaseTask{}, err
	}

	// Every step may read the workspace; see runPhase for why the allowlist
	// is path scoping, not a capability boundary.
	allowlist := []string{
		storage.IssueDir(projectID, issueID),
		storage.SourcePath(projectID, issueID),
		storage.WorkspacePath(projectID, issueID),
	}

	inputPaths := []string{storage.IssueMarkdownPath(projectID, issueID)}
	if names, err := e.listAttachmentNames(ctx, projectID, issueID); err == nil {
		for _, name := range names {
			inputPaths = append(inputPaths, storage.AttachmentPath(projectID, issueID, name))
		}
	}
	if prevKey != "" {
		res, err := readResult(ctx, e.store, storage.ResultPath(projectID, issueID, prevKey))
		if err == nil && res.LatestOutput != "" {
			inputPaths = append(inputPaths, res.LatestOutput)
		}
	}

	return PhaseTask{
		AgentType:         cfg.ID,
		StepKey:           phase,
		Flow:              flow,
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
// plus the preceding step's accepted output (prevKey "" for the first step;
// prevAgent is that step's agent id, for labeling).
func (e *Engine) buildBaseInput(ctx context.Context, projectID, issueID int64, phase, prevKey, prevAgent, issueTitle, description string) (string, error) {
	// Prefer SQLite description; fall back to empty if unset (legacy issues).
	names, _ := e.listAttachmentNames(ctx, projectID, issueID)
	input := buildIssueUserInput(issueTitle, description, names)
	if prevKey == "" {
		return input, nil
	}
	res, err := readResult(ctx, e.store, storage.ResultPath(projectID, issueID, prevKey))
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
	input += fmt.Sprintf("\n\nAccepted step output (%s agent):\n%s", prevAgent, string(data))
	return input, nil
}

// pathGuide tells the agent how tool paths work for this issue. Without it,
// models guess names like "attempts" or "." against the storage root and
// loop on "path not allowed". canEdit marks steps whose agent has editing
// tools — it only changes the workspace wording, not the allowlist: every
// step may read the workspace.
func pathGuide(projectID, issueID int64, phase string, canEdit bool, allowlist []string) string {
	issueDir := storage.IssueDir(projectID, issueID)
	source := storage.SourcePath(projectID, issueID)
	var b strings.Builder
	b.WriteString("\n\n## Path guide for tools\n")
	b.WriteString("Tool paths are relative to the shared workspace (short names like `source`), ")
	b.WriteString("or full storage keys under the allowlist. Path traversal (`..`) is rejected.\n")
	ws := storage.WorkspacePath(projectID, issueID)
	b.WriteString(fmt.Sprintf("- Workspace (default base, shared across steps): `%s`\n", ws))
	if canEdit {
		b.WriteString("  Mutable: you have file-editing tools — write and modify files here.\n")
	} else {
		b.WriteString("  Read-only for you: no editing tools — inspect what earlier steps wrote, do not modify.\n")
	}
	b.WriteString(fmt.Sprintf("- Issue root (issue.md, attachments, per-step results): `%s`\n", issueDir))
	b.WriteString(fmt.Sprintf("- Source snapshot (read-only, start here): `%s` or `source`\n", source))
	b.WriteString("- Allowlist prefixes:\n")
	for _, a := range allowlist {
		b.WriteString(fmt.Sprintf("  - `%s`\n", a))
	}
	return b.String()
}

// humanGateFeedback returns feedback a human left when retrying a cleared
// scope hold: Decide writes attempts/1/feedback.md and removes
// result.json, so the retry starts at attempt 1 with no rejected output to
// build retry context from.
func (e *Engine) humanGateFeedback(ctx context.Context, projectID, issueID int64, phase string) string {
	data, err := e.store.Read(ctx, storage.FeedbackPath(projectID, issueID, phase, 1))
	if err != nil || len(data) == 0 {
		return ""
	}
	return string(data)
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

// CurrentStepState returns the current step of the issue flow and its status
// from the filesystem. steps come from the issue's pipeline_json.
func (e *Engine) CurrentStepState(projectID, issueID int64, steps []sqlite.Step) (string, string, error) {
	return e.currentStepState(projectID, issueID, steps)
}

func (e *Engine) currentStepState(projectID, issueID int64, steps []sqlite.Step) (string, string, error) {
	for _, step := range steps {
		result, err := readResult(context.Background(), e.store, storage.ResultPath(projectID, issueID, step.Key))
		if err != nil {
			// No result.json yet means this step hasn't run.
			return step.Key, "in_progress", nil
		}
		switch result.Status {
		case "done":
			continue
		case "":
			return step.Key, "in_progress", nil
		default:
			return step.Key, result.Status, nil
		}
	}
	return steps[len(steps)-1].Key, "done", nil
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
	// Serialize against chat turns (and sibling issues) mutating the same
	// bare cache; a concurrent `worktree prune` can race `worktree add`.
	lock := gitWorkspaceLocks.lock(projectID)
	lock.Lock()
	defer lock.Unlock()
	mgr := &gorchgit.Manager{StorageRoot: e.cfg.StorageRoot}
	if err := mgr.EnsureCache(ctx, projectID, cfg); err != nil {
		return err
	}
	abs := storage.Abs(e.cfg.StorageRoot, storage.SourcePath(projectID, issueID))
	return mgr.CreateSourceWorktree(ctx, projectID, abs, cfg)
}

// prepareIssueWorkspaceOnce creates the issue-level mutable workspace: a git
// worktree on the per-issue branch when the project is git-backed, otherwise a
// seeded copy of the source snapshot. It runs once per issue — the first step
// whose agent has editing tools — and later steps reuse what is already
// there (a prepared workspace is never re-seeded or re-created).
func (e *Engine) prepareIssueWorkspaceOnce(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue) error {
	gitCfg, err := e.projectGitConfig(project)
	if err != nil {
		return err
	}
	// Resolve through the workspaceKey fallback so a legacy issue resumed
	// mid-flight keeps working in its existing implementation/workspace
	// instead of getting a fresh empty tree at the new path.
	wsKey, err := e.workspaceKey(ctx, issue)
	if err != nil {
		return err
	}
	if gitCfg.Enabled() {
		abs := storage.Abs(e.cfg.StorageRoot, wsKey)
		// A worktree is already checked out at this path when a previous step
		// prepared it.
		if dirNonEmpty(abs) {
			return nil
		}
		mgr := &gorchgit.Manager{StorageRoot: e.cfg.StorageRoot}
		branch := gorchgit.IssueBranchName(issue.ID)
		// Lock only the git mutations.
		lock := gitWorkspaceLocks.lock(project.ID)
		lock.Lock()
		if err := mgr.EnsureCache(ctx, project.ID, gitCfg); err != nil {
			lock.Unlock()
			return err
		}
		if err := mgr.CreateImplementerWorktree(ctx, project.ID, abs, branch, gitCfg); err != nil {
			lock.Unlock()
			return err
		}
		lock.Unlock()
		return nil
	}
	// No git: seed once from the frozen source snapshot. A non-empty workspace
	// is left alone (idempotency across steps and retries).
	if dirNonEmpty(storage.Abs(e.cfg.StorageRoot, wsKey)) {
		return nil
	}
	return e.seedWorkspace(ctx, project.ID, issue.ID, storage.SourcePath(project.ID, issue.ID), wsKey)
}

// dirNonEmpty reports whether the directory exists and has at least one entry.
func dirNonEmpty(abs string) bool {
	dirs, err := os.ReadDir(abs)
	return err == nil && len(dirs) > 0
}

// commitIssueWorkspace commits the issue workspace after a step the human
// accepted, so each accepted step lands in git history in order. No-op when
// nothing changed or the project is not git-backed.
func (e *Engine) commitIssueWorkspace(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, stepKey string) error {
	gitCfg, err := e.projectGitConfig(project)
	if err != nil {
		return err
	}
	if !gitCfg.Enabled() {
		return nil
	}
	wsKey, err := e.workspaceKey(ctx, issue)
	if err != nil {
		return err
	}
	abs := storage.Abs(e.cfg.StorageRoot, wsKey)
	mgr := &gorchgit.Manager{StorageRoot: e.cfg.StorageRoot}
	msg := gorchgit.CommitMessage(issue.Title, issue.ID, stepKey)
	created, err := mgr.CommitAll(ctx, abs, msg, gitCfg.AuthorName, gitCfg.AuthorEmail)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	branch := gorchgit.IssueBranchName(issue.ID)
	if gitCfg.Push {
		if err := mgr.Push(ctx, abs, branch); err != nil {
			return err
		}
	}
	if gitCfg.CreatePR {
		body := fmt.Sprintf("Automated PR for issue #%d (%s).", issue.ID, stepKey)
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

// seedWorkspace copies the source snapshot into the issue workspace.
// If the source snapshot does not exist (no project source configured), the
// workspace is left empty.
func (e *Engine) seedWorkspace(ctx context.Context, projectID, issueID int64, sourceSnapshotPath, destPath string) error {
	exists, err := e.store.Exists(ctx, sourceSnapshotPath)
	if err != nil {
		return fmt.Errorf("check source snapshot: %w", err)
	}
	if !exists {
		return nil
	}
	return copyStorageDir(ctx, e.store, sourceSnapshotPath, destPath)
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

// chatSourceTTL bounds how stale a chat source worktree may be before a
// chat turn re-fetches and re-checks it out. A chat conversation must not
// `git fetch` on every model turn, but a chat agent reading a days-old tree
// is its own bug: never reuse a stale worktree silently.
const chatSourceTTL = 15 * time.Minute

// chatSourceRoot returns an absolute host directory the chat drawer may read
// for this project, materializing a project-level read-only git worktree
// when the project is git-mode. The returned refresh flag reports whether
// the worktree was (re)created, for logging and tests.
//
//	source_path set  -> that host directory (unchanged behaviour)
//	git.repo_url set -> EnsureCache + CreateSourceWorktree at
//	                    storage.Abs(root, storage.ChatSourcePath(projectID))
//	neither          -> "", false, nil (caller must surface a clear chat error)
//
// The worktree is refreshed when it is missing or its mtime is older than
// chatSourceTTL; otherwise the existing checkout is reused as-is. The
// per-project git workspace lock serializes the fetch/worktree mutation
// against pipeline phases on the same bare cache.
func (e *Engine) chatSourceRoot(ctx context.Context, project *sqlite.Project, pc config.ProjectConfig) (abs string, refreshed bool, err error) {
	if pc.SourcePath != "" {
		return pc.SourcePath, false, nil
	}
	gitCfg, err := e.projectGitConfig(project)
	if err != nil {
		return "", false, err
	}
	if !gitCfg.Enabled() {
		return "", false, nil
	}
	lock := gitWorkspaceLocks.lock(project.ID)
	lock.Lock()
	defer lock.Unlock()

	abs = storage.Abs(e.cfg.StorageRoot, storage.ChatSourcePath(project.ID))
	if st, statErr := os.Stat(abs); statErr == nil && st.IsDir() && time.Since(st.ModTime()) < chatSourceTTL {
		return abs, false, nil
	}
	mgr := &gorchgit.Manager{StorageRoot: e.cfg.StorageRoot}
	if err := mgr.EnsureCache(ctx, project.ID, gitCfg); err != nil {
		return "", false, err
	}
	if err := mgr.CreateSourceWorktree(ctx, project.ID, abs, gitCfg); err != nil {
		// Unborn/empty source repos cascade into `worktree add: invalid
		// reference`; surface that as a readable chat error rather than a raw
		// git error.
		if strings.Contains(err.Error(), "invalid reference") {
			return "", false, fmt.Errorf("project source repo has no commits; chat cannot read it")
		}
		return "", false, err
	}
	return abs, true, nil
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

func modelTimeout(cfg config.ModelConfig) time.Duration {
	if cfg.Timeout == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		log.Printf("model timeout %q unparseable (%v); falling back to 60s", cfg.Timeout, err)
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
		declarer, ok := t.(interface {
			Declaration() *genai.FunctionDeclaration
		})
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

func (e *Engine) observeEscalation(ctx context.Context, projectName string, issueID int64, phase, errMsg string) {
	if e.escalator == nil {
		return
	}
	when := notify.ClassifyFailure(errMsg)
	e.escalator.Observe(ctx, notify.Event{
		When:    when,
		Project: projectName,
		IssueID: issueID,
		Phase:   phase,
		Message: errMsg,
	})
}
