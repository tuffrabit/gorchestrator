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
		"foo": {SourcePath: sourceDir},
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

func TestSubmitIssue_AgentFlavors_FrozenAndApplied(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {
			Agents: map[string]config.ProjectAgentConfig{
				"researcher": {
					Default: "thorough",
					Flavors: map[string]config.AgentConfig{
						"thorough": {
							Model:              config.ModelConfig{Provider: "dryrun", Model: "thorough-model"},
							SystemPromptAppend: "Prefer root-cause depth.",
						},
						"cheap": {
							Model: config.ModelConfig{Provider: "dryrun", Model: "cheap-model"},
						},
					},
				},
			},
		},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName:  "acme",
		IssueTitle:   "cast me",
		DryRun:       true,
		AgentFlavors: map[string]string{"researcher": "cheap"},
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	cast := parseIssueCast(issue.AgentFlavorsJSON)
	if cast["researcher"] != "cheap" {
		t.Fatalf("cast = %#v, want researcher=cheap", cast)
	}

	// Defaults fill other types with no flavors → empty.
	if cast["planner"] != "" {
		t.Fatalf("unexpected planner cast %q", cast["planner"])
	}

	// Resolved agent config uses the frozen cast (before dry-run display override).
	project, err := eng.projects.Get(issue.ProjectID)
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	ac, err := eng.agentConfigForIssue(project, issue, "research")
	if err != nil {
		t.Fatalf("agentConfigForIssue: %v", err)
	}
	if ac.Model.Model != "cheap-model" {
		t.Fatalf("research model = %q, want cheap-model", ac.Model.Model)
	}

	// Pipeline still runs; task.json may force dryrun provider when DryRun=true.
	_ = eng.Issues().UpdateStatus(issue.ID, sqlite.StatusInProgress, "research")
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	// Cast must be unchanged after pipeline (retries/crash recovery reuse it).
	reloaded, err := eng.Issues().Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AgentFlavorsJSON != issue.AgentFlavorsJSON {
		t.Fatalf("cast changed after process: %q → %q", issue.AgentFlavorsJSON, reloaded.AgentFlavorsJSON)
	}
}

func TestSubmitIssue_InvalidFlavor(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {
			Agents: map[string]config.ProjectAgentConfig{
				"researcher": {
					Default: "a",
					Flavors: map[string]config.AgentConfig{
						"a": {},
						"b": {},
					},
				},
			},
		},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	_, err = eng.SubmitIssue(ctx, RunOptions{
		ProjectName:  "acme",
		IssueTitle:   "bad cast",
		DryRun:       true,
		AgentFlavors: map[string]string{"researcher": "nope"},
	})
	if err == nil || !strings.Contains(err.Error(), "flavor") {
		t.Fatalf("expected flavor error, got %v", err)
	}
}

func TestSubmitIssue_DefaultFlavorWhenOmitted(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {
			Agents: map[string]config.ProjectAgentConfig{
				"planner": {
					Default: "standard",
					Flavors: map[string]config.AgentConfig{
						"standard": {Model: config.ModelConfig{Model: "std-model"}},
						"fast":     {Model: config.ModelConfig{Model: "fast-model"}},
					},
				},
			},
		},
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
	cast := parseIssueCast(issue.AgentFlavorsJSON)
	if cast["planner"] != "standard" {
		t.Fatalf("cast planner = %q, want standard", cast["planner"])
	}
}

func TestAgentConfigForIssue_ExternalForcesHuman(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {
			Agents: map[string]config.ProjectAgentConfig{
				"implementer": {
					Default: "coder",
					Flavors: map[string]config.AgentConfig{
						"coder": {Adjudicator: "self"},
					},
				},
			},
		},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(context.Background(), RunOptions{
		ProjectName: "acme",
		IssueTitle:  "ext",
		DryRun:      true,
		Source:      "webhook",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, _ := eng.projects.Get(issue.ProjectID)
	ac, err := eng.agentConfigForIssue(project, issue, "implementation")
	if err != nil {
		t.Fatal(err)
	}
	if ac.Adjudicator != "human" {
		t.Fatalf("adjudicator = %q, want human for external", ac.Adjudicator)
	}
}

func TestAgentConfigForIssue_MissingFlavorAfterYAMLEdit(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Projects = map[string]config.ProjectConfig{
		"acme": {
			Agents: map[string]config.ProjectAgentConfig{
				"researcher": {
					Default: "cheap",
					Flavors: map[string]config.AgentConfig{
						"cheap":    {},
						"thorough": {},
					},
				},
			},
		},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := eng.SubmitIssue(context.Background(), RunOptions{
		ProjectName:  "acme",
		IssueTitle:   "x",
		DryRun:       true,
		AgentFlavors: map[string]string{"researcher": "thorough"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate YAML edit deleting the flavor from live config.
	cfg.Projects["acme"] = config.ProjectConfig{
		Agents: map[string]config.ProjectAgentConfig{
			"researcher": {
				Default: "cheap",
				Flavors: map[string]config.AgentConfig{"cheap": {}},
			},
		},
	}
	project, _ := eng.projects.Get(issue.ProjectID)
	_, err = eng.agentConfigForIssue(project, issue, "research")
	if err == nil || !strings.Contains(err.Error(), "thorough") {
		t.Fatalf("expected missing flavor error, got %v", err)
	}
	eng.Close()
}
