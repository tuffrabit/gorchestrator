package sqlite

import (
	"path/filepath"
	"testing"
)

func TestParsePipeline(t *testing.T) {
	steps, err := ParsePipeline(`["scout","architect","coder","reviewer"]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(steps))
	}
	for i, s := range steps {
		wantKey := map[int]string{0: "step-1", 1: "step-2", 2: "step-3", 3: "step-4"}[i]
		wantID := map[int]string{0: "scout", 1: "architect", 2: "coder", 3: "reviewer"}[i]
		if s.Index != i+1 || s.Key != wantKey || s.AgentID != wantID {
			t.Fatalf("step %d = %+v, want {Index:%d Key:%s AgentID:%s}", i, s, i+1, wantKey, wantID)
		}
	}

	if steps, err := ParsePipeline(``); err != nil || len(steps) != 0 {
		t.Fatalf("empty pipeline: steps=%v err=%v, want none", steps, err)
	}
	if steps, err := ParsePipeline(`[]`); err != nil || len(steps) != 0 {
		t.Fatalf("[] pipeline: steps=%v err=%v, want none", steps, err)
	}
	if _, err := ParsePipeline(`not-json`); err == nil {
		t.Fatal("invalid JSON should error")
	}
	if _, err := ParsePipeline(`{"a":"b"}`); err == nil {
		t.Fatal("non-array JSON should error")
	}
}

func TestLegacyPipeline(t *testing.T) {
	want := []Step{
		{Index: 1, Key: "research", AgentID: "researcher"},
		{Index: 2, Key: "plan", AgentID: "planner"},
		{Index: 3, Key: "implementation", AgentID: "implementer"},
	}
	got := LegacyPipeline()
	if len(got) != 3 {
		t.Fatalf("LegacyPipeline() = %+v, want 3 steps", got)
	}
	for i, s := range got {
		if s != want[i] {
			t.Fatalf("legacy step %d = %+v, want %+v", i, s, want[i])
		}
	}
}

func TestStepsForIssue(t *testing.T) {
	// Flow issue.
	issue := &Issue{PipelineJSON: `["coder","reviewer"]`}
	steps, err := StepsForIssue(issue)
	if err != nil {
		t.Fatalf("StepsForIssue: %v", err)
	}
	if len(steps) != 2 || steps[0].Key != "step-1" || steps[0].AgentID != "coder" || steps[1].Key != "step-2" || steps[1].AgentID != "reviewer" {
		t.Fatalf("steps = %+v", steps)
	}

	// Legacy issue: empty pipeline_json falls back to the fixed pipeline.
	legacy := &Issue{PipelineJSON: "", AgentFlavorsJSON: `{"researcher":"cheap"}`}
	steps, err = StepsForIssue(legacy)
	if err != nil {
		t.Fatalf("StepsForIssue(legacy): %v", err)
	}
	if len(steps) != 3 || steps[0].Key != "research" || steps[2].Key != "implementation" {
		t.Fatalf("legacy steps = %+v", steps)
	}
}

func TestPipelineJSONRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	issues := NewIssueRepo(db)
	projects := NewProjectRepo(db)

	p, err := projects.Create("acme")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// New flow issue: current_phase is the first step key.
	issue, err := issues.Create(p.ID, "add auth", "step-1", "")
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if issue.CurrentPhase != "step-1" {
		t.Fatalf("current_phase = %q, want step-1", issue.CurrentPhase)
	}
	if issue.PipelineJSON != "[]" {
		t.Fatalf("pipeline_json = %q, want []", issue.PipelineJSON)
	}

	// Update the frozen flow, then verify the round trip.
	const raw = `["scout","coder"]`
	if _, err := db.Exec(`UPDATE issues SET pipeline_json = ? WHERE id = ?`, raw, issue.ID); err != nil {
		t.Fatalf("update pipeline: %v", err)
	}
	got, err := issues.Get(issue.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PipelineJSON != raw {
		t.Fatalf("pipeline_json = %q, want %q", got.PipelineJSON, raw)
	}
	steps, err := StepsForIssue(got)
	if err != nil {
		t.Fatalf("steps for issue: %v", err)
	}
	if len(steps) != 2 || steps[0].AgentID != "scout" || steps[1].AgentID != "coder" {
		t.Fatalf("steps = %+v", steps)
	}

	// current_phase is always taken from the caller, never the column default.
	legacyIssue, err := issues.CreateQueued(p.ID, "legacy", "research", "", false)
	if err != nil {
		t.Fatalf("create queued: %v", err)
	}
	if legacyIssue.CurrentPhase != "research" {
		t.Fatalf("legacy current_phase = %q, want research", legacyIssue.CurrentPhase)
	}
}
