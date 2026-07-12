package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestEffectiveEffort_MissingIsHigh(t *testing.T) {
	if EffectiveEffort("") != EffortHigh {
		t.Fatal(EffectiveEffort(""))
	}
	if EffectiveEffort("nope") != EffortHigh {
		t.Fatal(EffectiveEffort("nope"))
	}
	if EffectiveEffort("medium") != EffortMedium {
		t.Fatal(EffectiveEffort("medium"))
	}
}

func TestEffortRequiresGate(t *testing.T) {
	if !EffortRequiresGate("high", "high") {
		t.Fatal("high should gate at min high")
	}
	if EffortRequiresGate("low", "high") {
		t.Fatal("low should not gate at min high")
	}
	if !EffortRequiresGate("medium", "medium") {
		t.Fatal("medium should gate at min medium")
	}
	if EffortRequiresGate("low", "medium") {
		t.Fatal("low should not gate at min medium")
	}
}

func TestProcessIssue_HighEffortHoldsBeforeImplement(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	// Default effort_gate_min is high via typed project empty config.
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Ship feature [effort:high]",
		Description: "Implement with dry-run high effort marker.",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("status = %q", issue.Status)
	}

	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	issue, err = eng.Issues().Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("status = %q, want waiting_human", issue.Status)
	}
	if issue.CurrentPhase != "implementation" {
		t.Fatalf("phase = %q, want implementation", issue.CurrentPhase)
	}

	// Plan must be done with effort high.
	plan, err := readResult(ctx, eng.store, storage.ResultPath(issue.ProjectID, issue.ID, "plan"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "done" {
		t.Fatalf("plan status = %q", plan.Status)
	}
	if EffectiveEffort(plan.Effort) != EffortHigh {
		t.Fatalf("plan effort = %q, want high", plan.Effort)
	}

	impl, err := readResult(ctx, eng.store, storage.ResultPath(issue.ProjectID, issue.ID, "implementation"))
	if err != nil {
		t.Fatal(err)
	}
	if impl.Status != "waiting_human" || !IsEffortHoldError(impl.Error) {
		t.Fatalf("impl result = %+v", impl)
	}

	view, err := eng.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEffortHoldError(view.HoldReason) {
		t.Fatalf("HoldReason = %q", view.HoldReason)
	}

	// Pass should clear hold and allow implementer to run to completion.
	if err := eng.Decide(ctx, DecideOptions{
		IssueID:   issue.ID,
		Decision:  "pass",
		Feedback:  "approved high effort",
		DecidedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusQueued {
		t.Fatalf("after pass status = %q", issue.Status)
	}
	exists, _ := eng.store.Exists(ctx, storage.ResultPath(issue.ProjectID, issue.ID, "implementation"))
	if exists {
		t.Fatal("implementation hold result should be cleared")
	}

	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue after pass: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("final status = %q, want done", issue.Status)
	}
}

func TestProcessIssue_LowEffortNoHold(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Tiny fix",
		Description: "No effort marker — dry-run defaults to low.",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatal(err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("status = %q, want done (no effort hold)", issue.Status)
	}
}

func TestProcessIssue_MediumGatesWhenConfigured(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects["acme"] = config.ProjectConfig{
		Guardrails: config.ProjectGuardrails{EffortGateMin: "medium"},
	}
	// Re-normalize so defaults apply if empty — LoadFrom not used; set explicitly.
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Medium work [effort:medium]",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatal(err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("status = %q, want waiting_human for medium gate", issue.Status)
	}
	impl, err := readResult(ctx, eng.store, storage.ResultPath(issue.ProjectID, issue.ID, "implementation"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(impl.Error, "medium") {
		t.Fatalf("error = %q", impl.Error)
	}
}

func TestIsEffortHoldError(t *testing.T) {
	if !IsEffortHoldError("effort: high (gate_min=high)") {
		t.Fatal("expected true")
	}
	if IsPrePhaseHoldError("tool boom") {
		t.Fatal("expected false")
	}
	if !IsPrePhaseHoldError("scope: x") || !IsPrePhaseHoldError("effort: high") {
		t.Fatal("pre-phase holds")
	}
}
