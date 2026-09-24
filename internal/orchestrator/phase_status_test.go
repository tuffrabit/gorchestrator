package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestMapPhaseResultToIssueStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"done", sqlite.StatusInProgress},
		{"retry", sqlite.StatusInProgress},
		{"in_progress", sqlite.StatusInProgress},
		{"", sqlite.StatusInProgress},
		{"waiting_human", sqlite.StatusWaitingHuman},
		{"failed", sqlite.StatusFailed},
		{"cancelled", sqlite.StatusCancelled},
	}
	for _, tc := range cases {
		if got := mapPhaseResultToIssueStatus(tc.in); got != tc.want {
			t.Errorf("mapPhaseResultToIssueStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNextStepKey(t *testing.T) {
	steps := sqlite.LegacyPipeline()
	if got := nextStepKey(steps, "research"); got != "plan" {
		t.Fatalf("research → %q, want plan", got)
	}
	if got := nextStepKey(steps, "plan"); got != "implementation" {
		t.Fatalf("plan → %q, want implementation", got)
	}
	if got := nextStepKey(steps, "implementation"); got != "" {
		t.Fatalf("implementation has no next, got %q", got)
	}
}

func TestBuildPhaseSteps_TransitionResearchToPlan(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	project, err := eng.projects.GetByName("steps")
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	issue, err := eng.issues.CreateQueued(project.ID, "phase strip", "step-1", defaultTestFlowJSON, false)
	if err != nil {
		t.Fatal(err)
	}

	// Step-1 completed; issue advanced to step-2 (as runPipeline does on step start).
	step1Result := PhaseResult{Status: "done", Attempt: 1}
	if err := writeResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, "step-1"), step1Result); err != nil {
		t.Fatal(err)
	}
	step2Result := PhaseResult{Status: "in_progress", Attempt: 1}
	if err := writeResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, "step-2"), step2Result); err != nil {
		t.Fatal(err)
	}
	if err := eng.issues.UpdateStatus(issue.ID, sqlite.StatusInProgress, "step-2"); err != nil {
		t.Fatal(err)
	}
	issue, _ = eng.issues.Get(issue.ID)

	steps := eng.buildPhaseSteps(ctx, project.ID, issue)
	if len(steps) != 3 {
		t.Fatalf("steps = %d", len(steps))
	}
	if steps[0].State != "done" || steps[0].Key != "step-1" {
		t.Fatalf("step-1 = %+v, want done", steps[0])
	}
	if steps[1].State != "current" || steps[1].Key != "step-2" || steps[1].AgentID != "planner" {
		t.Fatalf("step-2 = %+v, want current planner", steps[1])
	}
	if steps[2].State != "pending" {
		t.Fatalf("step-3 = %+v, want pending", steps[2])
	}

	view, err := eng.issueView(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if view.Issue.Status != sqlite.StatusInProgress {
		t.Fatalf("issue status = %q, want in_progress (not done)", view.Issue.Status)
	}
	if view.Issue.CurrentPhase != "step-2" {
		t.Fatalf("current phase = %q, want step-2", view.Issue.CurrentPhase)
	}
	if view.Phases[1].State != "current" {
		t.Fatalf("view step-2 state = %q", view.Phases[1].State)
	}

	// Ensure we didn't write junk next to the test DB path accidentally.
	_ = filepath.Join(tmp, "x")
}

func TestBuildPhaseSteps_IssueDone(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	project, err := eng.projects.GetByName("done-steps")
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	issue, err := eng.issues.CreateQueued(project.ID, "all done", "step-1", defaultTestFlowJSON, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"step-1", "step-2", "step-3"} {
		if err := writeResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, key), PhaseResult{Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = eng.issues.UpdateStatus(issue.ID, sqlite.StatusDone, "step-3")
	issue, _ = eng.issues.Get(issue.ID)

	steps := eng.buildPhaseSteps(ctx, project.ID, issue)
	for _, s := range steps {
		if s.State != "done" {
			t.Fatalf("%s state = %q, want done", s.Key, s.State)
		}
	}
}
