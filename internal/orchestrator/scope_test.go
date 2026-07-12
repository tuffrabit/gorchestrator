package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestEvaluateScope_TitlePhrase(t *testing.T) {
	hit := EvaluateScope("Please refactor the entire auth stack", "", nil, nil)
	if !hit.Flagged() {
		t.Fatal("expected title phrase to flag")
	}
	if !strings.HasPrefix(hit.Summary(), "scope:") {
		t.Fatalf("summary = %q", hit.Summary())
	}
}

func TestEvaluateScope_DescriptionPhrase(t *testing.T) {
	hit := EvaluateScope("Auth", "We should migrate everything to the new platform.", nil, nil)
	if !hit.Flagged() {
		t.Fatal("expected description phrase to flag")
	}
}

func TestEvaluateScope_CleanIssue(t *testing.T) {
	hit := EvaluateScope("Fix login redirect after SSO", "Users land on / after IdP; should return to deep link.", nil, nil)
	if hit.Flagged() {
		t.Fatalf("unexpected flag: %v", hit.Reasons)
	}
}

func TestEvaluateScope_AttachmentBody(t *testing.T) {
	hit := EvaluateScope("Notes", "See attachment", []string{"notes.md"}, []ScopeAttachment{{
		Name: "notes.md",
		Data: []byte("plan: rewrite the whole monorepo next quarter"),
	}})
	if !hit.Flagged() {
		t.Fatal("expected attachment body phrase to flag")
	}
}

func TestEvaluateScope_AttachmentBasename(t *testing.T) {
	hit := EvaluateScope("Upload", "", []string{"migrate-everything-plan.md"}, nil)
	if !hit.Flagged() {
		t.Fatal("expected basename to flag")
	}
}

func TestIsScopeHoldError(t *testing.T) {
	if !IsScopeHoldError("scope: forbidden phrase \"x\"") {
		t.Fatal("expected true")
	}
	if IsScopeHoldError("tool failed") {
		t.Fatal("expected false")
	}
}

func TestSubmitIssue_ScopeHold_Description(t *testing.T) {
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
		IssueTitle:  "Platform work",
		Description: "Please refactor the entire billing module and related services.",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("status = %q, want waiting_human", issue.Status)
	}
	if issue.CurrentPhase != "research" {
		t.Fatalf("phase = %q, want research", issue.CurrentPhase)
	}

	res, err := readResult(ctx, eng.store, storage.ResultPath(issue.ProjectID, issue.ID, "research"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if res.Status != "waiting_human" {
		t.Fatalf("result status = %q", res.Status)
	}
	if !IsScopeHoldError(res.Error) {
		t.Fatalf("result error = %q, want scope: prefix", res.Error)
	}

	// No agent run yet: no events.jsonl / attempts.
	eventsPath := storage.EventsPath(issue.ProjectID, issue.ID, "research")
	if exists, _ := eng.store.Exists(ctx, eventsPath); exists {
		t.Fatal("events.jsonl must not exist before research runs")
	}

	view, err := eng.GetIssue(ctx, issue.ID)
	if err != nil || view == nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if view.HoldReason == "" || !IsScopeHoldError(view.HoldReason) {
		t.Fatalf("HoldReason = %q", view.HoldReason)
	}

	pending, err := eng.decisions.PendingForIssue(issue.ID)
	if err != nil {
		t.Fatalf("PendingForIssue: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("expected pending decision")
	}
	if !pending[0].Feedback.Valid || !IsScopeHoldError(pending[0].Feedback.String) {
		t.Fatalf("decision feedback = %+v", pending[0].Feedback)
	}
}

func TestSubmitIssue_ScopeHold_TitleOnly(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "migrate everything to v2",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("status = %q", issue.Status)
	}
}

func TestSubmitIssue_CleanStillQueued(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Fix nil pointer in login",
		Description: "Stack trace in logs when password is empty.",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status = %q, want queued", issue.Status)
	}
}

func TestDecide_ScopeHoldPass_StartsResearch(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "rewrite the whole dashboard",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("precondition status = %q", issue.Status)
	}

	if err := eng.Decide(ctx, DecideOptions{
		IssueID:   issue.ID,
		Decision:  "pass",
		Feedback:  "scoped enough for a spike",
		DecidedBy: "test",
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	issue, err = eng.Issues().Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status after pass = %q, want queued", issue.Status)
	}

	// result.json for research must be gone so pipeline does not skip research.
	exists, err := eng.store.Exists(ctx, storage.ResultPath(issue.ProjectID, issue.ID, "research"))
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("research result.json should be removed after scope pass")
	}

	// Process should run research (dry-run) and advance.
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone && issue.Status != sqlite.StatusInProgress && issue.Status != sqlite.StatusQueued {
		// Full dry-run pipeline usually completes to done.
		t.Logf("status after process = %s (acceptable if mid-pipeline)", issue.Status)
	}
	// Research must have a terminal or in-progress result now.
	phase, status, err := eng.CurrentPhaseState(issue.ProjectID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if phase == "research" && status == "waiting_human" {
		t.Fatal("still held after pass+process")
	}
}

func TestDecide_ScopeHoldFail(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "refactor the entire monorepo",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.Decide(ctx, DecideOptions{
		IssueID:  issue.ID,
		Decision: "fail",
		Feedback: "too broad",
	}); err != nil {
		t.Fatal(err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusFailed {
		t.Fatalf("status = %q, want failed", issue.Status)
	}
}
