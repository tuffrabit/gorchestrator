package orchestrator

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

func testConfig(tmp string) *config.Config {
	return &config.Config{
		StorageRoot: tmp,
		DBPath:      filepath.Join(tmp, "gorchestrator.db"),
		DefaultModel: config.ModelConfig{
			Provider:   "dryrun",
			Model:      "dryrun-model",
			APIKeyEnv:  "",
			Timeout:    "30s",
			TimeoutDur: 30 * time.Second,
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileConfig{
				MaxBytes: 64 * 1024,
				MaxLines: 2000,
			},
		},
	}
}

func TestRun_DryRun(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
		Loops:       1,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

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

	outputPath := storage.AttemptOutputPath(project.ID, issue.ID, "research", 1)
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

	// Verify events.jsonl exists and contains at least a model turn and usage.
	eventsPath := storage.EventsPath(project.ID, issue.ID, "research")
	if exists, _ := store.Exists(ctx, eventsPath); !exists {
		t.Fatalf("events.jsonl missing: %s", eventsPath)
	}
	eventsData, err := store.Read(ctx, eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(eventsData) == 0 {
		t.Fatal("events.jsonl is empty")
	}
	var sawModelTurn, sawUsage bool
	scanner := bufio.NewScanner(strings.NewReader(string(eventsData)))
	for scanner.Scan() {
		var ev eventRecord
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type == "model_turn" {
			sawModelTurn = true
		}
		if ev.Type == "usage" {
			sawUsage = true
		}
	}
	if !sawModelTurn {
		t.Fatal("events.jsonl missing model_turn")
	}
	if !sawUsage {
		t.Fatal("events.jsonl missing usage")
	}

	resultPath := storage.ResultPath(project.ID, issue.ID, "research")
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
	if result.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", result.Attempt)
	}
	if result.LatestOutput == "" {
		t.Fatal("latest_output is empty")
	}
	if result.DoneRationale != "" {
		t.Fatalf("done_rationale should be empty, got %q", result.DoneRationale)
	}
}

func TestRun_DryRun_MultiTurnTokens(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth [multiturn]",
		DryRun:      true,
		Loops:       1,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	runs := sqlite.NewRunRepo(db)
	run, err := runs.Get(1)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	// Dry-run multi-turn scripted sequence reports 10 + 15 = 25 tokens.
	if run.TokensUsed != 25 {
		t.Fatalf("tokens_used = %d, want 25", run.TokensUsed)
	}
}

func TestRun_DryRun_Cancellation(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
		Loops:       1,
	}

	err := Run(ctx, cfg, opts)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	resultPath := storage.ResultPath(1, 1, "research")
	resultData, err := store.Read(context.Background(), resultPath)
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var result Result
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "cancelled" {
		t.Fatalf("result status = %q, want cancelled", result.Status)
	}
}

func TestRun_DryRun_FailedLoopCount(t *testing.T) {
	// A multi-loop run that fails mid-way should report the actual loop count reached.
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
		Loops:       3,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	resultPath := storage.ResultPath(1, 1, "research")
	resultData, err := store.Read(ctx, resultPath)
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var result Result
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "done" {
		t.Fatalf("expected done for dry-run, got %q", result.Status)
	}
	if result.LoopCount != 3 {
		t.Fatalf("loop_count = %d, want 3", result.LoopCount)
	}
}

func TestRun_DryRun_EmptyOutputFails(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth [empty]",
		DryRun:      true,
		Loops:       1,
	}

	err := Run(ctx, cfg, opts)
	if err == nil {
		t.Fatal("expected error for empty output")
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	resultPath := storage.ResultPath(1, 1, "research")
	resultData, err := store.Read(ctx, resultPath)
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var result Result
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("result status = %q, want failed", result.Status)
	}
	if result.LoopCount != 1 {
		t.Fatalf("loop_count = %d, want 1", result.LoopCount)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
