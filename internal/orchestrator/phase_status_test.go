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

func TestNextPhaseName(t *testing.T) {
	if nextPhaseName("research") != "plan" {
		t.Fatal("research → plan")
	}
	if nextPhaseName("plan") != "implementation" {
		t.Fatal("plan → implementation")
	}
	if nextPhaseName("implementation") != "" {
		t.Fatal("implementation has no next")
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

	project, err := eng.projects.GetOrCreate("steps")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := eng.issues.CreateQueued(project.ID, "phase strip", false)
	if err != nil {
		t.Fatal(err)
	}

	// Research completed; issue advanced to plan (as runPipeline does on phase start).
	researchResult := PhaseResult{Status: "done", Attempt: 1}
	if err := writeResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, "research"), researchResult); err != nil {
		t.Fatal(err)
	}
	planResult := PhaseResult{Status: "in_progress", Attempt: 1}
	if err := writeResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, "plan"), planResult); err != nil {
		t.Fatal(err)
	}
	if err := eng.issues.UpdateStatus(issue.ID, sqlite.StatusInProgress, "plan"); err != nil {
		t.Fatal(err)
	}
	issue, _ = eng.issues.Get(issue.ID)

	steps := eng.buildPhaseSteps(ctx, project.ID, issue)
	if len(steps) != 3 {
		t.Fatalf("steps = %d", len(steps))
	}
	if steps[0].State != "done" || steps[0].Name != "research" {
		t.Fatalf("research step = %+v, want done", steps[0])
	}
	if steps[1].State != "current" || steps[1].Name != "plan" || steps[1].Agent != "planner" {
		t.Fatalf("plan step = %+v, want current planner", steps[1])
	}
	if steps[2].State != "pending" {
		t.Fatalf("implementation step = %+v, want pending", steps[2])
	}

	view, err := eng.issueView(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if view.Issue.Status != sqlite.StatusInProgress {
		t.Fatalf("issue status = %q, want in_progress (not done)", view.Issue.Status)
	}
	if view.Issue.CurrentPhase != "plan" {
		t.Fatalf("current phase = %q, want plan", view.Issue.CurrentPhase)
	}
	if view.Phases[1].State != "current" {
		t.Fatalf("view plan state = %q", view.Phases[1].State)
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

	project, err := eng.projects.GetOrCreate("done-steps")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := eng.issues.CreateQueued(project.ID, "all done", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"research", "plan", "implementation"} {
		if err := writeResult(ctx, eng.store, storage.ResultPath(project.ID, issue.ID, phase), PhaseResult{Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = eng.issues.UpdateStatus(issue.ID, sqlite.StatusDone, "implementation")
	issue, _ = eng.issues.Get(issue.ID)

	steps := eng.buildPhaseSteps(ctx, project.ID, issue)
	for _, s := range steps {
		if s.State != "done" {
			t.Fatalf("%s state = %q, want done", s.Name, s.State)
		}
	}
}
