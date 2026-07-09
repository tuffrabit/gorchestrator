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
		Agents: map[string]config.AgentConfig{
			"researcher": {
				Adjudicator: "self",
				MaxAttempts: 1,
				Loops:       1,
			},
			"planner": {
				Adjudicator: "self",
				MaxAttempts: 1,
				Loops:       1,
			},
			"implementer": {
				Adjudicator: "self",
				MaxAttempts: 1,
				Loops:       1,
			},
		},
	}
}

func TestRun_DryRun_Pipeline(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
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
	if issue.Status != "done" {
		t.Fatalf("issue status = %q, want done", issue.Status)
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

	for _, phase := range []string{"research", "plan", "implementation"} {
		taskPath := storage.TaskPath(project.ID, issue.ID, phase)
		if exists, _ := store.Exists(ctx, taskPath); !exists {
			t.Fatalf("%s task.json missing: %s", phase, taskPath)
		}
		taskData, err := store.Read(ctx, taskPath)
		if err != nil {
			t.Fatalf("read %s task.json: %v", phase, err)
		}
		var task PhaseTask
		if err := json.Unmarshal(taskData, &task); err != nil {
			t.Fatalf("parse %s task.json: %v", phase, err)
		}
		if task.AgentType != phaseAgentType(phase) {
			t.Fatalf("%s agent_type = %q, want %q", phase, task.AgentType, phaseAgentType(phase))
		}
		if task.Adjudicator != "self" {
			t.Fatalf("%s adjudicator = %q, want self", phase, task.Adjudicator)
		}

		outputPath := storage.AttemptOutputPath(project.ID, issue.ID, phase, 1)
		if phase != "implementation" {
			if exists, _ := store.Exists(ctx, outputPath); !exists {
				t.Fatalf("%s output.md missing: %s", phase, outputPath)
			}
			outputData, err := store.Read(ctx, outputPath)
			if err != nil {
				t.Fatalf("read %s output.md: %v", phase, err)
			}
			if len(outputData) == 0 {
				t.Fatalf("%s output.md is empty", phase)
			}
		}

		eventsPath := storage.EventsPath(project.ID, issue.ID, phase)
		if exists, _ := store.Exists(ctx, eventsPath); !exists {
			t.Fatalf("%s events.jsonl missing: %s", phase, eventsPath)
		}
		eventsData, err := store.Read(ctx, eventsPath)
		if err != nil {
			t.Fatalf("read %s events.jsonl: %v", phase, err)
		}
		if len(eventsData) == 0 {
			t.Fatalf("%s events.jsonl is empty", phase)
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
			t.Fatalf("%s events.jsonl missing model_turn", phase)
		}
		if !sawUsage {
			t.Fatalf("%s events.jsonl missing usage", phase)
		}

		resultPath := storage.ResultPath(project.ID, issue.ID, phase)
		resultData, err := store.Read(ctx, resultPath)
		if err != nil {
			t.Fatalf("read %s result.json: %v", phase, err)
		}
		var result PhaseResult
		if err := json.Unmarshal(resultData, &result); err != nil {
			t.Fatalf("parse %s result.json: %v", phase, err)
		}
		if result.Status != "done" {
			t.Fatalf("%s result status = %q, want done", phase, result.Status)
		}
		if result.Attempt != 1 {
			t.Fatalf("%s attempt = %d, want 1", phase, result.Attempt)
		}
		if result.LatestOutput == "" {
			t.Fatalf("%s latest_output is empty", phase)
		}
	}

	// The implementer workspace should exist and contain the dryrun file.
	wsPath := storage.WorkspacePath(project.ID, issue.ID)
	if exists, _ := store.Exists(ctx, filepath.Join(wsPath, "dryrun.go")); !exists {
		t.Fatalf("implementer workspace missing dryrun.go")
	}

	// Each phase should have recorded token usage (write_output + finish_task = 25).
	run, err = runs.Get(1)
	if err != nil {
		t.Fatalf("get research run: %v", err)
	}
	if run.TokensUsed != 25 {
		t.Fatalf("research tokens = %d, want 25", run.TokensUsed)
	}
}

func TestRun_DryRun_SourceSnapshot_ExcludesGit(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	sourceDir := filepath.Join(tmp, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatalf("write .git/config: %v", err)
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		SourcePath:  sourceDir,
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	snapshotPath := storage.SourcePath(1, 1)
	if exists, _ := store.Exists(ctx, filepath.Join(snapshotPath, "main.go")); !exists {
		t.Fatalf("source snapshot missing main.go")
	}
	if exists, _ := store.Exists(ctx, filepath.Join(snapshotPath, ".git", "config")); exists {
		t.Fatalf("source snapshot should exclude .git")
	}

	wsPath := storage.WorkspacePath(1, 1)
	if exists, _ := store.Exists(ctx, filepath.Join(wsPath, "main.go")); !exists {
		t.Fatalf("workspace missing seeded main.go")
	}
	if exists, _ := store.Exists(ctx, filepath.Join(wsPath, ".git", "config")); exists {
		t.Fatalf("workspace should not contain .git")
	}
}

func TestRun_DryRun_SourceSnapshot(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	sourceDir := filepath.Join(tmp, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		SourcePath:  sourceDir,
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	snapshotPath := storage.SourcePath(1, 1)
	if exists, _ := store.Exists(ctx, filepath.Join(snapshotPath, "main.go")); !exists {
		t.Fatalf("source snapshot missing main.go")
	}

	wsPath := storage.WorkspacePath(1, 1)
	if exists, _ := store.Exists(ctx, filepath.Join(wsPath, "main.go")); !exists {
		t.Fatalf("workspace missing seeded main.go")
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
	var result PhaseResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "cancelled" {
		t.Fatalf("result status = %q, want cancelled", result.Status)
	}
}

func TestResume_RetryWithFeedback(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	// Configure research to use human adjudication so the pipeline pauses.
	cfg.Agents["researcher"] = config.AgentConfig{
		Adjudicator: "human",
		MaxAttempts: 3,
		Loops:       1,
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	issues := sqlite.NewIssueRepo(db)
	issue, err := issues.Get(1)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "waiting_human" {
		t.Fatalf("issue status = %q, want waiting_human", issue.Status)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	feedback := "missing tests"
	resumeOpts := ResumeOptions{
		ProjectName: "foo",
		IssueID:     issue.ID,
		Decision:    "retry",
		Feedback:    feedback,
	}
	if err := Resume(ctx, cfg, resumeOpts); err != nil {
		t.Fatalf("Resume retry failed: %v", err)
	}

	// After retry, the phase re-runs and pauses again for human adjudication.
	issue, err = issues.Get(1)
	if err != nil {
		t.Fatalf("get issue after retry: %v", err)
	}
	if issue.Status != "waiting_human" {
		t.Fatalf("issue status after retry = %q, want waiting_human", issue.Status)
	}

	feedbackPath := storage.FeedbackPath(1, 1, "research", 1)
	fbData, err := store.Read(ctx, feedbackPath)
	if err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	if string(fbData) != feedback {
		t.Fatalf("feedback = %q, want %q", string(fbData), feedback)
	}

	// A second attempt directory should exist.
	attempt2Output := storage.AttemptOutputPath(1, 1, "research", 2)
	if exists, _ := store.Exists(ctx, attempt2Output); !exists {
		t.Fatalf("attempt 2 output missing")
	}

	// Pass to finish the pipeline.
	resumeOpts.Decision = "pass"
	resumeOpts.Feedback = "looks good"
	if err := Resume(ctx, cfg, resumeOpts); err != nil {
		t.Fatalf("Resume pass failed: %v", err)
	}

	issue, err = issues.Get(1)
	if err != nil {
		t.Fatalf("get issue after pass: %v", err)
	}
	if issue.Status != "done" {
		t.Fatalf("issue status after pass = %q, want done", issue.Status)
	}
}

func TestResume_Pass(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	cfg.Agents["researcher"] = config.AgentConfig{
		Adjudicator: "human",
		MaxAttempts: 1,
		Loops:       1,
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	resumeOpts := ResumeOptions{
		ProjectName: "foo",
		IssueID:     1,
		Decision:    "pass",
		Feedback:    "looks good",
	}
	if err := Resume(ctx, cfg, resumeOpts); err != nil {
		t.Fatalf("Resume failed: %v", err)
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
	var result PhaseResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "done" {
		t.Fatalf("research result status = %q, want done", result.Status)
	}
	if result.DoneRationale != "looks good" {
		t.Fatalf("done_rationale = %q, want looks good", result.DoneRationale)
	}
}

func TestResume_Fail(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	cfg.Agents["researcher"] = config.AgentConfig{
		Adjudicator: "human",
		MaxAttempts: 1,
		Loops:       1,
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	resumeOpts := ResumeOptions{
		ProjectName: "foo",
		IssueID:     1,
		Decision:    "fail",
		Feedback:    "irrelevant research",
	}
	// Fail is a clean terminal decision — Resume returns nil after applying it.
	if err := Resume(ctx, cfg, resumeOpts); err != nil {
		t.Fatalf("Resume fail: %v", err)
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
	var result PhaseResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("research result status = %q, want failed", result.Status)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	issue, err := sqlite.NewIssueRepo(db).Get(1)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Status != "failed" {
		t.Fatalf("issue status = %q, want failed", issue.Status)
	}
}

func TestCrashRecovery_RerunInProgressPhase(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	// Create a project and issue manually, snapshot source, and fake an in-progress research phase.
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	defer eng.Close()

	project, err := eng.projects.GetOrCreate("foo")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	issue, err := eng.issues.Create(project.ID, "add auth")
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sourceDir := filepath.Join(tmp, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := eng.snapshotSource(ctx, project.ID, issue.ID, sourceDir); err != nil {
		t.Fatalf("snapshot source: %v", err)
	}

	resultPath := storage.ResultPath(project.ID, issue.ID, "research")
	if err := writeResult(ctx, eng.store, resultPath, PhaseResult{
		Status:    "in_progress",
		Attempt:   1,
		LoopCount: 0,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("write in-progress result: %v", err)
	}

	if err := eng.runPipeline(ctx, project, issue, true); err != nil {
		t.Fatalf("runPipeline failed: %v", err)
	}

	result, err := readResult(ctx, eng.store, resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if result.Status != "done" {
		t.Fatalf("research result status = %q, want done", result.Status)
	}

	// Pipeline should have continued through plan and implementation.
	implResultPath := storage.ResultPath(project.ID, issue.ID, "implementation")
	implResult, err := readResult(ctx, eng.store, implResultPath)
	if err != nil {
		t.Fatalf("read implementation result: %v", err)
	}
	if implResult.Status != "done" {
		t.Fatalf("implementation result status = %q, want done", implResult.Status)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
