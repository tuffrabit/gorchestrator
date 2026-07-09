package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/notify"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestSubmitIssue_QueuesWithoutRunning(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Add OIDC",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status = %q, want queued", issue.Status)
	}
	if !issue.DryRun {
		t.Fatal("expected dry_run=true")
	}

	// Pipeline must not have run yet.
	phase, status, err := eng.CurrentPhaseState(issue.ProjectID, issue.ID)
	if err != nil {
		t.Fatalf("CurrentPhaseState: %v", err)
	}
	if phase != "research" || status != "in_progress" {
		// no result.json yet → in_progress from currentPhaseState
		if status != "in_progress" {
			t.Fatalf("phase state = %s/%s", phase, status)
		}
	}
}

func TestProcessIssue_DryRunCompletes(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Add OIDC",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusInProgress, "research")

	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	issue, err = eng.Issues().Get(issue.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("status = %q, want done", issue.Status)
	}
}

func TestRecoverAll_RequeuesInProgress(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "crash me",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	// Simulate a crash: leave as in_progress with no result.json terminal.
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusInProgress, "research")

	if err := eng.RecoverAll(ctx); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status after recover = %q, want queued", issue.Status)
	}
}

func TestDecide_RetryRequeues(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Agents["researcher"] = config.AgentConfig{
		Adjudicator: "human",
		MaxAttempts: 3,
		Loops:       1,
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()
	eng.SetNotifier(notify.NewDispatcher(eng.Notifications(), &notify.Console{}))

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "needs human",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusInProgress, "research")
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("status = %q, want waiting_human", issue.Status)
	}

	// Console notification should have been recorded.
	rows, err := eng.Notifications().ListRecent(10)
	if err != nil {
		t.Fatalf("notifications: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected human_gate notification")
	}

	if err := eng.Decide(ctx, DecideOptions{
		IssueID:   issue.ID,
		Decision:  "retry",
		Feedback:  "add more detail",
		DecidedBy: "test",
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status after retry = %q, want queued", issue.Status)
	}
}

func TestDecide_RetryFromFailed(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	// Self adjudicator + forced fail via cancelled context mid-run is awkward;
	// seed a failed phase on disk instead.
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "recover me",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}

	// Write a failed research result and mark the issue failed.
	resultPath := storage.ResultPath(issue.ProjectID, issue.ID, "research")
	if err := eng.Store().Write(ctx, resultPath, []byte(`{
		"status": "failed",
		"error": "model timed out",
		"attempt": 1,
		"loop_count": 1,
		"tokens_used": 10,
		"timestamp": "2026-01-01T00:00:00Z"
	}`)); err != nil {
		t.Fatalf("write result: %v", err)
	}
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusFailed, "research")

	if err := eng.Decide(ctx, DecideOptions{
		IssueID:   issue.ID,
		Decision:  "retry",
		Feedback:  "try again with more context about auth",
		DecidedBy: "test",
	}); err != nil {
		t.Fatalf("Decide retry from failed: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status after retry = %q, want queued", issue.Status)
	}

	// Worker path: process should re-run research (dry-run) and complete.
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusInProgress, "research")
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue after failed-retry: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("status after reprocess = %q, want done", issue.Status)
	}

	// Attempt 2 should exist (attempt 1 was the failed one).
	attempt2 := storage.AttemptOutputPath(issue.ProjectID, issue.ID, "research", 2)
	if exists, _ := eng.Store().Exists(ctx, attempt2); !exists {
		t.Fatalf("expected attempt 2 output at %s", attempt2)
	}
	fb, err := eng.Store().Read(ctx, storage.FeedbackPath(issue.ProjectID, issue.ID, "research", 1))
	if err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	if string(fb) != "try again with more context about auth" {
		t.Fatalf("feedback = %q", string(fb))
	}
}

func TestEventBus_Subscribe(t *testing.T) {
	bus := NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx, EventFilter{})
	bus.Publish(Event{Type: EventIssueSubmitted, IssueID: 1})
	select {
	case ev := <-ch:
		if ev.Type != EventIssueSubmitted {
			t.Fatalf("type = %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}
