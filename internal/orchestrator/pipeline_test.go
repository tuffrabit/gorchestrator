package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// TestRun_1StepFlow runs a single-agent flow (step-1 only) end to end.
func TestRun_1StepFlow(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "single agent job",
		Flow:        []string{"researcher"},
		DryRun:      true,
	}
	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	pid, iid := firstIssueIDs(t, cfg.DBPath)
	issue, err := sqlite.NewIssueRepo(db).Get(iid)
	if err != nil || issue == nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("issue status = %q, want done", issue.Status)
	}
	if issue.CurrentPhase != "step-1" {
		t.Fatalf("issue current_phase = %q, want step-1", issue.CurrentPhase)
	}
	steps, err := sqlite.StepsForIssue(issue)
	if err != nil {
		t.Fatalf("parse pipeline: %v", err)
	}
	if len(steps) != 1 || steps[0].Key != "step-1" || steps[0].AgentID != "researcher" {
		t.Fatalf("unexpected steps: %+v", steps)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	// Only step-1 should have artifacts — no step-2 task.json.
	if exists, _ := store.Exists(ctx, storage.TaskPath(pid, iid, "step-2")); exists {
		t.Fatal("step-2 task.json should not exist for a 1-step flow")
	}
	resultData, err := store.Read(ctx, storage.ResultPath(pid, iid, "step-1"))
	if err != nil {
		t.Fatalf("read step-1 result: %v", err)
	}
	var result PhaseResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Status != "done" {
		t.Fatalf("step-1 status = %q, want done", result.Status)
	}
}

// TestRun_4StepFlow runs a four-agent flow and checks each step artifact.
func TestRun_4StepFlow(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	flow := []string{"researcher", "planner", "implementer", "writer"}
	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "four step job",
		Flow:        flow,
		DryRun:      true,
	}
	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	pid, iid := firstIssueIDs(t, cfg.DBPath)
	issue, err := sqlite.NewIssueRepo(db).Get(iid)
	if err != nil || issue == nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("issue status = %q, want done", issue.Status)
	}
	if issue.CurrentPhase != "step-4" {
		t.Fatalf("issue current_phase = %q, want step-4", issue.CurrentPhase)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	wantAgents := map[string]string{"step-1": "researcher", "step-2": "planner", "step-3": "implementer", "step-4": "writer"}
	for i, key := range []string{"step-1", "step-2", "step-3", "step-4"} {
		taskData, err := store.Read(ctx, storage.TaskPath(pid, iid, key))
		if err != nil {
			t.Fatalf("read %s task.json: %v", key, err)
		}
		var task PhaseTask
		if err := json.Unmarshal(taskData, &task); err != nil {
			t.Fatalf("parse %s task.json: %v", key, err)
		}
		if task.AgentType != wantAgents[key] {
			t.Fatalf("%s agent_type = %q, want %q", key, task.AgentType, wantAgents[key])
		}
		if task.StepKey != key {
			t.Fatalf("%s step_key = %q, want %q", key, task.StepKey, key)
		}
		if i > 0 {
			prev := fmt.Sprintf("step-%d", i)
			prevOutput := storage.AttemptOutputPath(pid, iid, prev, 1)
			saw := false
			for _, p := range task.InputContextPaths {
				if p == prevOutput {
					saw = true
					break
				}
			}
			if !saw {
				t.Fatalf("%s input_context_paths missing prev step output %s: %v", key, prevOutput, task.InputContextPaths)
			}
		}
		if len(task.Flow) != 4 {
			t.Fatalf("%s flow = %v, want 4 agents", key, task.Flow)
		}
		// Each step writes a done result.
		resultData, err := store.Read(ctx, storage.ResultPath(pid, iid, key))
		if err != nil {
			t.Fatalf("read %s result: %v", key, err)
		}
		var result PhaseResult
		if err := json.Unmarshal(resultData, &result); err != nil {
			t.Fatalf("parse %s result: %v", key, err)
		}
		if result.Status != "done" {
			t.Fatalf("%s status = %q, want done", key, result.Status)
		}
	}
}

// TestRun_UnconfiguredAgentFails runs a flow that references an agent id with
// no config entry: the issue must end up failed, not hang or silently fall
// back to the built-in pipeline.
// TestRun_UnconfiguredAgentFails covers both failure surfaces of an
// unconfigured agent: a flow referencing an unknown agent is rejected at
// submit time, and an agent that disappears from config mid-flight fails the
// issue at that step.
func TestRun_UnconfiguredAgentFails(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	// 1) Submit-time validation: the flow references an agent with no config.
	broken := testConfig(t.TempDir())
	delete(broken.Agents, "implementer")
	err := Run(ctx, broken, RunOptions{
		ProjectName: "foo",
		IssueTitle:  "broken flow",
		Flow:        []string{"researcher", "planner", "implementer"},
		DryRun:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Run err = %v, want unconfigured-agent submit error", err)
	}

	// 2) Mid-flight config drift: the issue exists, then the agent is deleted
	// before the worker claims it.
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "foo",
		IssueTitle:  "drift",
		Flow:        []string{"researcher", "planner", "implementer"},
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	// Let the first two steps complete, then drop the implementer agent.
	delete(eng.cfg.Agents, "implementer")

	// Run the pipeline: steps 1-2 complete dry, step-3 fails on config lookup.
	if err := eng.ProcessIssue(ctx, issue.ID); err == nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	issue, err = eng.Issues().Get(issue.ID)
	if err != nil || issue == nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "failed" {
		t.Fatalf("issue status = %q, want failed", issue.Status)
	}
	if issue.CurrentPhase != "step-3" {
		t.Fatalf("issue current_phase = %q, want step-3", issue.CurrentPhase)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	events, err := store.Read(ctx, storage.EventsPath(issue.ProjectID, issue.ID, "step-3"))
	if err != nil {
		t.Fatalf("read step-3 events: %v", err)
	}
	if !strings.Contains(string(events), "not configured") {
		t.Fatalf("step-3 events should carry the unconfigured-agent error:\n%s", events)
	}
}

// TestRun_LegacyIssueRunsOldPipeline confirms an issue with empty
// pipeline_json still runs research → plan → implementation with the legacy
// phase directory names.
func TestRun_LegacyIssueRunsOldPipeline(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	defer eng.Close()

	project, err := eng.projects.GetByName("foo")
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	// Legacy issue: no pipeline_json.
	issue, err := eng.issues.Create(project.ID, "legacy job", "research", "")
	if err != nil {
		t.Fatalf("create legacy issue: %v", err)
	}
	if !sqlite.IsLegacyIssue(issue) {
		t.Fatal("issue should be legacy")
	}

	if err := eng.runPipeline(ctx, project, issue, true); err != nil {
		t.Fatalf("runPipeline failed: %v", err)
	}

	issue, err = eng.issues.Get(issue.ID)
	if err != nil || issue == nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("issue status = %q, want done", issue.Status)
	}
	if issue.CurrentPhase != "implementation" {
		t.Fatalf("issue current_phase = %q, want implementation", issue.CurrentPhase)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	for _, phase := range []string{"research", "plan", "implementation"} {
		if exists, _ := store.Exists(ctx, storage.ResultPath(project.ID, issue.ID, phase)); !exists {
			t.Fatalf("%s result.json missing for legacy issue", phase)
		}
	}
	// Legacy issues never get step-N artifacts.
	if exists, _ := store.Exists(ctx, storage.ResultPath(project.ID, issue.ID, "step-1")); exists {
		t.Fatal("step-1 result.json should not exist for a legacy issue")
	}
}

// TestResolveFlow covers fallback and validation rules.
func TestResolveFlow(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	defer eng.Close()

	// Explicit flow wins.
	flow, err := eng.resolveFlow("foo", []string{"researcher", "writer"})
	if err != nil {
		t.Fatalf("resolve explicit: %v", err)
	}
	if len(flow) != 2 || flow[0] != "researcher" || flow[1] != "writer" {
		t.Fatalf("explicit flow = %v", flow)
	}

	// Empty request falls back to project default_flow.
	flow, err = eng.resolveFlow("foo", nil)
	if err != nil {
		t.Fatalf("resolve fallback: %v", err)
	}
	if len(flow) != 3 {
		t.Fatalf("fallback flow = %v, want 3 agents", flow)
	}

	// Unknown agent id is a submit error.
	if _, err := eng.resolveFlow("foo", []string{"ghost"}); err == nil {
		t.Fatal("unknown agent id should fail")
	}

	// Duplicate agent id is a submit error.
	if _, err := eng.resolveFlow("foo", []string{"researcher", "researcher"}); err == nil {
		t.Fatal("duplicate agent id should fail")
	}

	// No flow and no default_flow is a submit error (no silent fallback).
	if _, err := eng.resolveFlow("ghost", nil); err == nil {
		t.Fatal("empty flow without default_flow should fail")
	}

	// Flow longer than maxFlowSteps is rejected.
	long := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		long = append(long, "researcher")
	}
	if _, err := eng.resolveFlow("foo", long); err == nil {
		t.Fatal("oversized flow should fail")
	}
}

// TestFlowNavigation helpers: keys, prev/next, agent ids.
func TestFlowNavigationHelpers(t *testing.T) {
	steps := []sqlite.Step{
		{Key: "step-1", AgentID: "researcher"},
		{Key: "step-2", AgentID: "planner"},
		{Key: "step-3", AgentID: "implementer"},
	}
	if got := flowAgentIDs(steps); len(got) != 3 || got[2] != "implementer" {
		t.Fatalf("flowAgentIDs = %v", got)
	}
	if got := nextStepKey(steps, "step-2"); got != "step-3" {
		t.Fatalf("nextStepKey(step-2) = %q", got)
	}
	if got := nextStepKey(steps, "step-3"); got != "" {
		t.Fatalf("nextStepKey(last) = %q, want empty", got)
	}
	if got := prevStepKey(steps, "step-2"); got != "step-1" {
		t.Fatalf("prevStepKey(step-2) = %q", got)
	}
	if got := prevStepKey(steps, "step-1"); got != "" {
		t.Fatalf("prevStepKey(first) = %q, want empty", got)
	}
	if got := stepIndex(steps, "step-2"); got != 1 {
		t.Fatalf("stepIndex(step-2) = %d", got)
	}
	if got := stepIndex(steps, "nope"); got != -1 {
		t.Fatalf("stepIndex(unknown) = %d", got)
	}
}

// TestMarshalFlowJSON round-trips through ParsePipeline.
func TestMarshalFlowJSON(t *testing.T) {
	jsonBytes, err := marshalFlowJSON([]string{"researcher", "writer"})
	if err != nil {
		t.Fatalf("marshalFlowJSON: %v", err)
	}
	steps, err := sqlite.ParsePipeline(jsonBytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(steps) != 2 || steps[1].Key != "step-2" || steps[1].AgentID != "writer" {
		t.Fatalf("unexpected steps: %+v", steps)
	}
}
