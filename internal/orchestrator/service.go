package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/adjudication"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// DecideOptions holds parameters for applying a human decision.
type DecideOptions struct {
	IssueID   int64
	Decision  string // pass | fail | retry
	Feedback  string
	Phase     string // optional; defaults to current phase
	DecidedBy string // stable user id, "local:admin", or "cli"
	UserID    *int64 // optional for audit
	Force     bool   // allow decide when not waiting_human (manual intervene)
}

// PhaseStep is one step in the research → plan → implementation strip.
type PhaseStep struct {
	Name   string // research | plan | implementation
	Agent  string // researcher | planner | implementer
	State  string // pending | current | done | failed | waiting
	Status string // raw phase result.json status (may be empty)
}

// IssueView is a read model for API/dashboard consumers.
type IssueView struct {
	Issue       *sqlite.Issue
	ProjectName string
	TokenTotal  int
	Attempt     int
	PhaseStatus string      // filesystem result status for current phase
	Phases      []PhaseStep // research → plan → implementation strip
}

// SubmitIssue creates the issue (snapshot source if configured), sets status
// queued, and returns immediately. Daemon workers pick it up.
func (e *Engine) SubmitIssue(ctx context.Context, opts RunOptions) (*sqlite.Issue, error) {
	if opts.ProjectName == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if opts.IssueTitle == "" {
		return nil, fmt.Errorf("issue title is required")
	}

	project, err := e.projects.GetOrCreate(opts.ProjectName)
	if err != nil {
		return nil, fmt.Errorf("get or create project: %w", err)
	}

	if opts.SourcePath != "" {
		if err := e.setProjectSourcePath(project, opts.SourcePath); err != nil {
			return nil, fmt.Errorf("set project source path: %w", err)
		}
	}

	source := opts.Source
	if source == "" {
		source = "manual"
	}
	issue, err := e.issues.CreateQueuedFrom(project.ID, opts.IssueTitle, opts.DryRun, source, opts.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	if err := e.prepareIssueSource(ctx, project, issue); err != nil {
		return nil, fmt.Errorf("prepare source: %w", err)
	}

	e.Publish(Event{
		Type:      EventIssueSubmitted,
		IssueID:   issue.ID,
		ProjectID: project.ID,
		Status:    issue.Status,
		Phase:     issue.CurrentPhase,
		Message:   issue.Title,
	})

	return issue, nil
}

// Decide applies a human decision, records decided_by, writes feedback.md on
// retry, and either terminalizes or re-queues the issue for the worker pool.
func (e *Engine) Decide(ctx context.Context, opts DecideOptions) error {
	issue, err := e.issues.Get(opts.IssueID)
	if err != nil || issue == nil {
		return fmt.Errorf("issue %d not found", opts.IssueID)
	}

	project, err := e.projects.Get(issue.ProjectID)
	if err != nil || project == nil {
		return fmt.Errorf("project for issue %d not found", opts.IssueID)
	}

	phase := opts.Phase
	if phase == "" {
		phase = issue.CurrentPhase
	}
	fsPhase, fsStatus, err := e.currentPhaseState(project.ID, issue.ID)
	if err != nil {
		return err
	}
	if phase == "" || phase == issue.CurrentPhase {
		phase = fsPhase
	}

	if !canHumanDecide(issue.Status, fsStatus, opts.Force) {
		return fmt.Errorf("issue %d cannot be decided in status %s (fs=%s); need waiting_human, failed, or cancelled",
			issue.ID, issue.Status, fsStatus)
	}

	// Reject empty/unknown before ParseOutcome, which maps unknowns to Fail.
	switch strings.ToLower(strings.TrimSpace(opts.Decision)) {
	case "pass", "fail", "retry":
	default:
		return fmt.Errorf("invalid decision %q; want pass|fail|retry", opts.Decision)
	}
	decision := adjudication.ParseOutcome(opts.Decision)
	if decision != adjudication.Pass && decision != adjudication.Fail && decision != adjudication.Retry {
		return fmt.Errorf("invalid decision %q; want pass|fail|retry", opts.Decision)
	}

	decidedBy := opts.DecidedBy
	if decidedBy == "" {
		decidedBy = "cli"
	}

	if err := e.applyHumanDecisionWithBy(ctx, project, issue, phase, opts.Decision, opts.Feedback, decidedBy); err != nil {
		return err
	}

	e.Publish(Event{
		Type:      EventDecisionApplied,
		IssueID:   issue.ID,
		ProjectID: project.ID,
		Phase:     phase,
		Status:    opts.Decision,
		Message:   opts.Feedback,
		Data: map[string]any{
			"decision":   opts.Decision,
			"decided_by": decidedBy,
		},
	})

	switch decision {
	case adjudication.Pass:
		// Mark phase done on FS already; re-queue so workers continue the pipeline
		// unless this was the last phase (runPipeline will mark issue done).
		if phase == "implementation" {
			_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusDone, phase)
			e.Publish(Event{
				Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
				Phase: phase, Status: sqlite.StatusDone,
			})
			return nil
		}
		_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusQueued, phase)
		e.Publish(Event{
			Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phase, Status: sqlite.StatusQueued,
		})
		return nil
	case adjudication.Fail:
		_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusFailed, phase)
		e.Publish(Event{
			Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phase, Status: sqlite.StatusFailed, Message: opts.Feedback,
		})
		return nil
	case adjudication.Retry:
		if err := e.issues.UpdateStatus(issue.ID, sqlite.StatusQueued, phase); err != nil {
			return fmt.Errorf("requeue issue: %w", err)
		}
		log.Printf("decide: issue %d phase %s retry → queued (worker will claim)", issue.ID, phase)
		e.Publish(Event{
			Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
			Phase: phase, Status: sqlite.StatusQueued, Message: "retry queued",
		})
		return nil
	}
	return nil
}

// canHumanDecide reports whether a human may pass/fail/retry this issue.
// Failed and cancelled are intentionally retryable so operators can inject
// better context and re-run; in_progress/queued/done require Force.
func canHumanDecide(issueStatus, fsStatus string, force bool) bool {
	if force {
		return true
	}
	switch issueStatus {
	case sqlite.StatusWaitingHuman, sqlite.StatusFailed, sqlite.StatusCancelled:
		return true
	}
	switch fsStatus {
	case "waiting_human", "failed", "cancelled", "retry":
		return true
	}
	return false
}

// applyHumanDecisionWithBy is like applyHumanDecision but records decided_by.
func (e *Engine) applyHumanDecisionWithBy(ctx context.Context, project *sqlite.Project, issue *sqlite.Issue, phase, decisionStr, feedback, decidedBy string) error {
	decision := adjudication.ParseOutcome(decisionStr)
	resultPath := storage.ResultPath(project.ID, issue.ID, phase)
	result, err := readResult(ctx, e.store, resultPath)
	if err != nil {
		// No result yet (edge case): synthesize a minimal envelope so retry can proceed.
		result = PhaseResult{Status: "failed", Attempt: 1, Timestamp: nowRFC3339()}
	}
	if result.Attempt <= 0 {
		result.Attempt = 1
	}

	if err := e.decisions.Record(issue.ID, phase, decisionStr, feedback, decidedBy); err != nil {
		log.Printf("failed to record decision: %v", err)
	}

	switch decision {
	case adjudication.Pass:
		result.Status = "done"
		result.DoneRationale = feedback
		result.Timestamp = nowRFC3339()
		return writeResult(ctx, e.store, resultPath, result)
	case adjudication.Fail:
		result.Status = "failed"
		result.Error = feedback
		result.Timestamp = nowRFC3339()
		return writeResult(ctx, e.store, resultPath, result)
	case adjudication.Retry:
		// Mark retry so runPhase starts attempt N+1 with feedback from this attempt.
		result.Status = "retry"
		result.Error = feedback
		result.Timestamp = nowRFC3339()
		feedbackPath := storage.FeedbackPath(project.ID, issue.ID, phase, result.Attempt)
		if err := e.store.Write(ctx, feedbackPath, []byte(feedback)); err != nil {
			return fmt.Errorf("write feedback: %w", err)
		}
		return writeResult(ctx, e.store, resultPath, result)
	default:
		return fmt.Errorf("invalid decision %q", decisionStr)
	}
}

// GetIssue returns a read model for one issue.
func (e *Engine) GetIssue(ctx context.Context, id int64) (*IssueView, error) {
	issue, err := e.issues.Get(id)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}
	return e.issueView(ctx, issue)
}

// ListIssues returns issue views matching the filter.
func (e *Engine) ListIssues(ctx context.Context, f sqlite.IssueListFilter) ([]*IssueView, error) {
	issues, err := e.issues.List(f)
	if err != nil {
		return nil, err
	}
	out := make([]*IssueView, 0, len(issues))
	for _, issue := range issues {
		v, err := e.issueView(ctx, issue)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ListProjects returns all projects.
func (e *Engine) ListProjects(ctx context.Context) ([]*sqlite.Project, error) {
	_ = ctx
	return e.projects.List()
}

// Subscribe returns a cancellable stream of daemon events for SSE.
func (e *Engine) Subscribe(ctx context.Context, filter EventFilter) <-chan Event {
	return e.bus.Subscribe(ctx, filter)
}

// Publish emits an event on the engine bus (no-op if bus is nil).
func (e *Engine) Publish(ev Event) {
	if e.bus != nil {
		e.bus.Publish(ev)
	}
}

// RecoverAll walks non-terminal issues, reconciles SQLite to filesystem truth
// (§9.4), re-queues recoverable work, and leaves waiting_human alone.
func (e *Engine) RecoverAll(ctx context.Context) error {
	issues, err := e.issues.ListNonTerminal()
	if err != nil {
		return fmt.Errorf("list non-terminal: %w", err)
	}
	for _, issue := range issues {
		if err := e.recoverIssue(ctx, issue); err != nil {
			log.Printf("recover issue %d: %v", issue.ID, err)
		}
	}
	return nil
}

func (e *Engine) recoverIssue(ctx context.Context, issue *sqlite.Issue) error {
	project, err := e.projects.Get(issue.ProjectID)
	if err != nil || project == nil {
		return fmt.Errorf("project %d not found", issue.ProjectID)
	}

	phase, fsStatus, err := e.currentPhaseState(project.ID, issue.ID)
	if err != nil {
		return err
	}

	// waiting_human: leave alone, surface in decision queue.
	if fsStatus == "waiting_human" || issue.Status == sqlite.StatusWaitingHuman {
		if issue.Status != sqlite.StatusWaitingHuman || issue.CurrentPhase != phase {
			_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusWaitingHuman, phase)
		}
		return nil
	}

	// Terminal filesystem states: reconcile SQLite.
	switch fsStatus {
	case "done":
		if phase == "implementation" {
			_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusDone, phase)
			return nil
		}
		// Intermediate phase done but pipeline not finished → re-queue.
		_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusQueued, phase)
		return nil
	case "failed", "cancelled":
		_ = e.issues.UpdateStatus(issue.ID, fsStatus, phase)
		return nil
	case "retry":
		// Adjudicator left a retry marker; re-queue to continue attempts.
		_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusQueued, phase)
		return nil
	}

	// in_progress (or missing result): crashed mid-phase or never started.
	// Re-queue for workers. runPhase will re-run the interrupted attempt.
	if issue.Status == sqlite.StatusQueued {
		// Already queued — ensure phase is reconciled.
		if issue.CurrentPhase != phase {
			_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusQueued, phase)
		}
		return nil
	}

	log.Printf("recovering issue %d phase %s (sqlite=%s fs=%s) → re-queue",
		issue.ID, phase, issue.Status, fsStatus)
	_ = e.issues.UpdateStatus(issue.ID, sqlite.StatusQueued, phase)
	e.Publish(Event{
		Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
		Phase: phase, Status: sqlite.StatusQueued, Message: "recovered",
	})
	_ = ctx
	return nil
}

// ProcessIssue runs the pipeline for a claimed (in_progress) issue.
// Used by daemon workers after ClaimQueued.
func (e *Engine) ProcessIssue(ctx context.Context, issueID int64) error {
	issue, err := e.issues.Get(issueID)
	if err != nil || issue == nil {
		return fmt.Errorf("issue %d not found", issueID)
	}
	project, err := e.projects.Get(issue.ProjectID)
	if err != nil || project == nil {
		return fmt.Errorf("project for issue %d not found", issueID)
	}

	e.Publish(Event{
		Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
		Phase: issue.CurrentPhase, Status: sqlite.StatusInProgress,
	})

	err = e.runPipeline(ctx, project, issue, issue.DryRun)

	// Refresh status after pipeline.
	issue, _ = e.issues.Get(issueID)
	if issue != nil {
		e.Publish(Event{
			Type: EventIssueStatus, IssueID: issue.ID, ProjectID: project.ID,
			Phase: issue.CurrentPhase, Status: issue.Status,
		})
	}
	return err
}

// Issues returns the issue repository (for daemon claim loop).
func (e *Engine) Issues() *sqlite.IssueRepo {
	return e.issues
}

// Store returns the storage port.
func (e *Engine) Store() storage.Port {
	return e.store
}

func (e *Engine) issueView(ctx context.Context, issue *sqlite.Issue) (*IssueView, error) {
	project, err := e.projects.Get(issue.ProjectID)
	if err != nil {
		return nil, err
	}
	name := ""
	if project != nil {
		name = project.Name
	}
	tokens, _ := e.runs.TokenTotalForIssue(issue.ID)
	phaseStatus := ""
	attempt := 0
	var phases []PhaseStep
	if project != nil {
		phase, status, err := e.currentPhaseState(project.ID, issue.ID)
		if err == nil {
			phaseStatus = status
			_ = phase
			res, err := readResult(ctx, e.store, storage.ResultPath(project.ID, issue.ID, issue.CurrentPhase))
			if err == nil {
				attempt = res.Attempt
				if phaseStatus == "" {
					phaseStatus = res.Status
				}
			}
		}
		phases = e.buildPhaseSteps(ctx, project.ID, issue)
	}
	return &IssueView{
		Issue:       issue,
		ProjectName: name,
		TokenTotal:  tokens,
		Attempt:     attempt,
		PhaseStatus: phaseStatus,
		Phases:      phases,
	}, nil
}

// buildPhaseSteps derives the dashboard phase strip from issue state + each
// phase's result.json. Completed phases show as done; the issue's current_phase
// shows as current while the issue is still active.
func (e *Engine) buildPhaseSteps(ctx context.Context, projectID int64, issue *sqlite.Issue) []PhaseStep {
	names := []string{"research", "plan", "implementation"}
	steps := make([]PhaseStep, 0, len(names))
	curIdx := indexOf(names, issue.CurrentPhase)

	for i, name := range names {
		step := PhaseStep{
			Name:  name,
			Agent: phaseAgentType(name),
			State: "pending",
		}
		if res, err := readResult(ctx, e.store, storage.ResultPath(projectID, issue.ID, name)); err == nil {
			step.Status = res.Status
		}

		switch {
		case issue.Status == sqlite.StatusDone:
			step.State = "done"
		case step.Status == "done":
			step.State = "done"
		case step.Status == "failed" || step.Status == "cancelled":
			step.State = "failed"
		case step.Status == "waiting_human" || (issue.Status == sqlite.StatusWaitingHuman && name == issue.CurrentPhase):
			step.State = "waiting"
		case name == issue.CurrentPhase && issue.Status != sqlite.StatusFailed && issue.Status != sqlite.StatusCancelled:
			// Active issue on this phase (running, queued mid-pipeline, or retrying).
			if step.Status == "done" {
				step.State = "done"
			} else {
				step.State = "current"
			}
		case curIdx >= 0 && i < curIdx:
			// Behind the current phase pointer — treat as completed unless FS says otherwise.
			if step.Status == "failed" || step.Status == "cancelled" {
				step.State = "failed"
			} else {
				step.State = "done"
			}
		default:
			step.State = "pending"
		}
		steps = append(steps, step)
	}
	return steps
}

// ArtifactPath resolves a relative artifact path under an issue directory.
// Rejects absolute paths and ".." segments.
func (e *Engine) ArtifactPath(issueID int64, rel string) (string, error) {
	issue, err := e.issues.Get(issueID)
	if err != nil || issue == nil {
		return "", fmt.Errorf("issue %d not found", issueID)
	}
	if err := storage.ValidateRelativePath(rel); err != nil {
		return "", err
	}
	base := storage.IssueDir(issue.ProjectID, issue.ID)
	return storage.JoinContained(base, rel)
}

// ReadArtifact returns artifact bytes for an issue-relative path.
func (e *Engine) ReadArtifact(ctx context.Context, issueID int64, rel string) ([]byte, string, error) {
	key, err := e.ArtifactPath(issueID, rel)
	if err != nil {
		return nil, "", err
	}
	data, err := e.store.Read(ctx, key)
	if err != nil {
		return nil, "", err
	}
	return data, key, nil
}

// IssueIDString is a helper for audit target ids.
func IssueIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}

// ErrIssueNotFound is returned when DeleteIssue targets a missing id.
var ErrIssueNotFound = fmt.Errorf("issue not found")

// DeleteIssue permanently removes an issue: its storage directory tree
// (projects/{pid}/issues/{iid}/…) and all related DB rows (issue, runs,
// decisions, notifications). Audit history is kept. This is a hard delete.
func (e *Engine) DeleteIssue(ctx context.Context, issueID int64) error {
	issue, err := e.issues.Get(issueID)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}
	if issue == nil {
		return ErrIssueNotFound
	}

	dir := storage.IssueDir(issue.ProjectID, issue.ID)
	if err := e.store.RemoveAll(ctx, dir); err != nil {
		return fmt.Errorf("remove issue storage %s: %w", dir, err)
	}

	if err := e.issues.Delete(issue.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIssueNotFound
		}
		return fmt.Errorf("delete issue row: %w", err)
	}

	e.Publish(Event{
		Type:      EventIssueDeleted,
		IssueID:   issue.ID,
		ProjectID: issue.ProjectID,
		Phase:     issue.CurrentPhase,
		Status:    "deleted",
		Message:   issue.Title,
	})
	return nil
}
