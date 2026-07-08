package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

func TestRun_DryRun(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	cfg := &config.Config{
		StorageRoot: tmp,
		AdaptersDir: filepath.Join(tmp, "adapters"),
		DBPath:      filepath.Join(tmp, "gorchestrator.db"),
		DefaultModel: config.ModelConfig{
			Provider:   "dryrun",
			Model:      "dryrun-model",
			APIKeyEnv:  "",
			Timeout:    "30s",
			TimeoutDur: 30 * time.Second,
		},
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
		Loops:       1,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify SQLite records.
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	projects := sqlite.NewProjectRepo(db)
	issues := sqlite.NewIssueRepo(db)
	runs := sqlite.NewRunRepo(db)

	project, err := projects.GetByName("foo")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if project == nil {
		t.Fatal("project not found")
	}

	issue, err := issues.Get(1)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue == nil {
		t.Fatal("issue not found")
	}
	if issue.Title != "add auth" {
		t.Fatalf("issue title = %q, want %q", issue.Title, "add auth")
	}

	run, err := runs.Get(1)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run == nil {
		t.Fatal("run not found")
	}
	if run.Status != "done" {
		t.Fatalf("run status = %q, want done", run.Status)
	}

	// Verify filesystem artifacts.
	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	taskPath := storage.TaskPath(project.ID, issue.ID, "research")
	if exists, _ := store.Exists(ctx, taskPath); !exists {
		t.Fatalf("task.json missing: %s", taskPath)
	}
	taskData, err := store.Read(ctx, taskPath)
	if err != nil {
		t.Fatalf("read task.json: %v", err)
	}
	var task Task
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatalf("parse task.json: %v", err)
	}
	if task.AgentType != "researcher" {
		t.Fatalf("agent_type = %q, want researcher", task.AgentType)
	}
	if task.NLoops != 1 {
		t.Fatalf("n_loops = %d, want 1", task.NLoops)
	}

	outputPath := storage.OutputPath(project.ID, issue.ID, "research")
	if exists, _ := store.Exists(ctx, outputPath); !exists {
		t.Fatalf("output.md missing: %s", outputPath)
	}
	outputData, err := store.Read(ctx, outputPath)
	if err != nil {
		t.Fatalf("read output.md: %v", err)
	}
	if len(outputData) == 0 {
		t.Fatal("output.md is empty")
	}

	resultPath := storage.ResultPath(project.ID, issue.ID, "research")
	if exists, _ := store.Exists(ctx, resultPath); !exists {
		t.Fatalf("result.json missing: %s", resultPath)
	}
	resultData, err := store.Read(ctx, resultPath)
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var result Result
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "done" {
		t.Fatalf("result status = %q, want done", result.Status)
	}
	if result.LoopCount != 1 {
		t.Fatalf("loop_count = %d, want 1", result.LoopCount)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
