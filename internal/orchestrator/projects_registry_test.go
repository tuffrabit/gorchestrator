package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestSyncProjects_CreatesAndUpdates(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {SourcePath: "/old/path"},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	p, err := eng.projects.GetByName("acme")
	if err != nil || p == nil {
		t.Fatalf("project missing after sync: %v", err)
	}
	var pc config.ProjectConfig
	if err := json.Unmarshal([]byte(p.ConfigJSON), &pc); err != nil {
		t.Fatal(err)
	}
	if pc.SourcePath != "/old/path" {
		t.Fatalf("source_path = %q", pc.SourcePath)
	}

	// Second start with updated YAML.
	eng.Close()
	cfg.Projects["acme"] = config.ProjectConfig{SourcePath: "/new/path"}
	eng2, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine2: %v", err)
	}
	defer eng2.Close()
	p2, _ := eng2.projects.GetByName("acme")
	if err := json.Unmarshal([]byte(p2.ConfigJSON), &pc); err != nil {
		t.Fatal(err)
	}
	if pc.SourcePath != "/new/path" {
		t.Fatalf("after re-sync source_path = %q, want /new/path", pc.SourcePath)
	}
	// Same row id (update, not recreate).
	if p2.ID != p.ID {
		t.Fatalf("project id changed %d → %d", p.ID, p2.ID)
	}
}

func TestSubmitIssue_UnknownProject_NoRow(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{"acme": {}}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	_, err = eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "other",
		IssueTitle:  "nope",
		DryRun:      true,
	})
	if err == nil {
		t.Fatal("expected error for unknown project")
	}
	if !strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("error = %v", err)
	}
	all, err := eng.projects.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.Name == "other" {
			t.Fatal("unknown project should not create a row")
		}
	}
}

func TestRun_KnownProject_UsesYAMLSourcePath(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"foo": {SourcePath: sourceDir, DefaultFlow: []string{"researcher", "planner", "implementer"}},
	}
	if err := Run(ctx, cfg, RunOptions{ProjectName: "foo", IssueTitle: "x", DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatal(err)
	}
	pid, iid := firstIssueIDs(t, cfg.DBPath)
	if exists, _ := store.Exists(ctx, filepath.Join(storage.SourcePath(pid, iid), "main.go")); !exists {
		t.Fatal("expected source snapshot from YAML source_path")
	}
}

func TestSubmitIssue_FlowFrozenAndApplied(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {DefaultFlow: []string{"researcher", "planner", "implementer"}},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	flow := []string{"researcher", "implementer"}
	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "flow me",
		Flow:        flow,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	steps, err := sqlite.ParsePipeline(issue.PipelineJSON)
	if err != nil {
		t.Fatalf("parse frozen flow: %v", err)
	}
	if len(steps) != 2 || steps[0].AgentID != "researcher" || steps[1].AgentID != "implementer" {
		t.Fatalf("frozen flow = %+v, want researcher→implementer", steps)
	}

	// Resolved step config uses the frozen flow.
	ac, err := eng.stepAgentConfig(issue, steps[1])
	if err != nil {
		t.Fatalf("stepAgentConfig: %v", err)
	}
	if ac.ID != "implementer" {
		t.Fatalf("step-2 agent = %q, want implementer", ac.ID)
	}

	// Pipeline still runs to completion.
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusInProgress, "step-1")
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	// Frozen flow must be unchanged after pipeline (retries/crash recovery reuse it).
	reloaded, err := eng.Issues().Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PipelineJSON != issue.PipelineJSON {
		t.Fatalf("flow changed after process: %q → %q", issue.PipelineJSON, reloaded.PipelineJSON)
	}
}

func TestSubmitIssue_InvalidAgentInFlow(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	_, err = eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "bad flow",
		Flow:        []string{"ghost"},
		DryRun:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected unconfigured agent error, got %v", err)
	}
}

func TestSubmitIssue_DefaultFlowWhenOmitted(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {DefaultFlow: []string{"researcher", "planner", "implementer"}},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "defaults",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steps, err := sqlite.ParsePipeline(issue.PipelineJSON)
	if err != nil {
		t.Fatalf("parse pipeline: %v", err)
	}
	if len(steps) != 3 || steps[1].AgentID != "planner" {
		t.Fatalf("default flow = %+v, want 3 agents with planner second", steps)
	}
}

func TestStepAgentConfig_ExternalForcesHuman(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {DefaultFlow: []string{"researcher", "planner", "implementer"}},
	}
	// Give the first step a read-only tool list so the external-trust rule
	// does not force it to a human gate.
	agent := cfg.Agents["researcher"]
	agent.Tools = []string{"read_file"}
	cfg.Agents["researcher"] = agent

	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(context.Background(), RunOptions{
		ProjectName: "acme",
		IssueTitle:  "ext",
		Source:      "webhook",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steps, err := sqlite.StepsForIssue(issue)
	if err != nil || len(steps) == 0 {
		t.Fatalf("steps: %v", err)
	}
	// Implementer is the editing step; external trigger forces a human gate.
	ac, err := eng.stepAgentConfig(issue, steps[2])
	if err != nil {
		t.Fatal(err)
	}
	if ac.Adjudicator != "human" {
		t.Fatalf("adjudicator = %q, want human for external", ac.Adjudicator)
	}
	// Non-editing steps keep their configured adjudicator.
	ac, err = eng.stepAgentConfig(issue, steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if ac.Adjudicator != "self" {
		t.Fatalf("step-1 adjudicator = %q, want self", ac.Adjudicator)
	}
}

func TestStepAgentConfig_UnconfiguredAfterYAMLEdit(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {DefaultFlow: []string{"researcher", "planner", "implementer"}},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := eng.SubmitIssue(context.Background(), RunOptions{
		ProjectName: "acme",
		IssueTitle:  "x",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a YAML edit deleting an agent from the live config.
	delete(cfg.Agents, "implementer")
	steps, err := sqlite.StepsForIssue(issue)
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.stepAgentConfig(issue, steps[2])
	if err == nil || !strings.Contains(err.Error(), "implementer") {
		t.Fatalf("expected unconfigured agent error, got %v", err)
	}
	eng.Close()
}
