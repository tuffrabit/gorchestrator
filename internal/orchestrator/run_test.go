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
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
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
				SystemPrompt: "Research the issue and report findings.",
				Adjudicator:  "self",
				MaxAttempts:  1,
				Loops:        1,
			},
			"planner": {
				SystemPrompt: "Plan the work as a checklist.",
				Adjudicator:  "self",
				MaxAttempts:  1,
				Loops:        1,
			},
			"implementer": {
				SystemPrompt: "Implement the plan in the workspace.",
				Adjudicator:  "self",
				MaxAttempts:  1,
				Loops:        1,
			},
			"writer": {
				SystemPrompt: "Write documentation for the change.",
				Adjudicator:  "self",
				MaxAttempts:  1,
				Loops:        1,
			},
			"reviewer": {
				SystemPrompt: "Review the changes.",
				Adjudicator:  "self",
				MaxAttempts:  1,
				Loops:        1,
			},
		},
		// Register common fixture projects used across orchestrator tests. Each
		// gets a default_flow so tests that don't pass an explicit flow work.
		Projects: map[string]config.ProjectConfig{
			"acme":       {DefaultFlow: []string{"researcher", "planner", "implementer"}},
			"foo":        {DefaultFlow: []string{"researcher", "planner", "implementer"}},
			"steps":      {DefaultFlow: []string{"researcher", "planner", "implementer", "writer"}},
			"done-steps": {DefaultFlow: []string{"researcher", "planner", "implementer"}},
			"delproj":    {DefaultFlow: []string{"researcher", "planner", "implementer"}},
			"gitproj":    {DefaultFlow: []string{"researcher", "planner", "implementer"}},
		},
	}
}

// firstIssueIDs returns project_id and issue id for the first issue row.
func firstIssueIDs(t *testing.T, dbPath string) (projectID, issueID int64) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	err = db.QueryRow(`SELECT project_id, id FROM issues ORDER BY id ASC LIMIT 1`).Scan(&projectID, &issueID)
	if err != nil {
		t.Fatalf("first issue: %v", err)
	}
	return projectID, issueID
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

	// pipeline_json must be persisted on the issue.
	if issue.PipelineJSON == "" {
		t.Fatal("issue.pipeline_json is empty")
	}
	if issue.CurrentPhase != "step-3" {
		t.Fatalf("issue current_phase = %q, want step-3", issue.CurrentPhase)
	}

	wantAgents := map[string]string{"step-1": "researcher", "step-2": "planner", "step-3": "implementer"}
	for _, phase := range []string{"step-1", "step-2", "step-3"} {
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
		if task.AgentType != wantAgents[phase] {
			t.Fatalf("%s agent_type = %q, want %q", phase, task.AgentType, wantAgents[phase])
		}
		if task.Adjudicator != "self" {
			t.Fatalf("%s adjudicator = %q, want self", phase, task.Adjudicator)
		}

		outputPath := storage.AttemptOutputPath(project.ID, issue.ID, phase, 1)
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

	cfg.Projects["foo"] = config.ProjectConfig{SourcePath: sourceDir, DefaultFlow: []string{"researcher", "planner", "implementer"}}
	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	pid, iid := firstIssueIDs(t, cfg.DBPath)
	snapshotPath := storage.SourcePath(pid, iid)
	if exists, _ := store.Exists(ctx, filepath.Join(snapshotPath, "main.go")); !exists {
		t.Fatalf("source snapshot missing main.go")
	}
	if exists, _ := store.Exists(ctx, filepath.Join(snapshotPath, ".git", "config")); exists {
		t.Fatalf("source snapshot should exclude .git")
	}

	wsPath := storage.WorkspacePath(pid, iid)
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

	cfg.Projects["foo"] = config.ProjectConfig{SourcePath: sourceDir, DefaultFlow: []string{"researcher", "planner", "implementer"}}
	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	store, err := storage.NewFS(tmp)
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	pid, iid := firstIssueIDs(t, cfg.DBPath)
	snapshotPath := storage.SourcePath(pid, iid)
	if exists, _ := store.Exists(ctx, filepath.Join(snapshotPath, "main.go")); !exists {
		t.Fatalf("source snapshot missing main.go")
	}

	wsPath := storage.WorkspacePath(pid, iid)
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

	pid, iid := firstIssueIDs(t, cfg.DBPath)
	resultPath := storage.ResultPath(pid, iid, "step-1")
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

func TestRun_DefaultAdjudicatorWaitsForHuman(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	// No adjudicator configured for the step-1 agent: the built-in default is a
	// human gate, so the pipeline pauses after step-1.
	cfg.Agents["researcher"] = config.AgentConfig{
		SystemPrompt: cfg.Agents["researcher"].SystemPrompt,
		MaxAttempts:  1,
		Loops:        1,
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

	_, iid := firstIssueIDs(t, cfg.DBPath)
	issue, err := sqlite.NewIssueRepo(db).Get(iid)
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
	resultPath := storage.ResultPath(issue.ProjectID, issue.ID, "step-1")
	resultData, err := store.Read(ctx, resultPath)
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var result PhaseResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if result.Status != "waiting_human" {
		t.Fatalf("research result status = %q, want waiting_human", result.Status)
	}
}

func TestResume_RetryWithFeedback(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	// Configure step-1 (researcher) to use human adjudication so the pipeline pauses.
	cfg.Agents["researcher"] = config.AgentConfig{
		SystemPrompt: cfg.Agents["researcher"].SystemPrompt,
		Adjudicator:  "human",
		MaxAttempts:  3,
		Loops:        1,
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
	_, iid := firstIssueIDs(t, cfg.DBPath)
	issue, err := issues.Get(iid)
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
	issue, err = issues.Get(iid)
	if err != nil {
		t.Fatalf("get issue after retry: %v", err)
	}
	if issue.Status != "waiting_human" {
		t.Fatalf("issue status after retry = %q, want waiting_human", issue.Status)
	}

	feedbackPath := storage.FeedbackPath(issue.ProjectID, issue.ID, "step-1", 1)
	fbData, err := store.Read(ctx, feedbackPath)
	if err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	if string(fbData) != feedback {
		t.Fatalf("feedback = %q, want %q", string(fbData), feedback)
	}

	// A second attempt directory should exist.
	attempt2Output := storage.AttemptOutputPath(issue.ProjectID, issue.ID, "step-1", 2)
	if exists, _ := store.Exists(ctx, attempt2Output); !exists {
		t.Fatalf("attempt 2 output missing")
	}

	// Pass to finish the pipeline.
	resumeOpts.Decision = "pass"
	resumeOpts.Feedback = "looks good"
	if err := Resume(ctx, cfg, resumeOpts); err != nil {
		t.Fatalf("Resume pass failed: %v", err)
	}

	issue, err = issues.Get(iid)
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
		SystemPrompt: cfg.Agents["researcher"].SystemPrompt,
		Adjudicator:  "human",
		MaxAttempts:  1,
		Loops:        1,
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	pid, iid := firstIssueIDs(t, cfg.DBPath)
	resumeOpts := ResumeOptions{
		ProjectName: "foo",
		IssueID:     iid,
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

	resultPath := storage.ResultPath(pid, iid, "step-1")
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
		SystemPrompt: cfg.Agents["researcher"].SystemPrompt,
		Adjudicator:  "human",
		MaxAttempts:  1,
		Loops:        1,
	}

	opts := RunOptions{
		ProjectName: "foo",
		IssueTitle:  "add auth",
		DryRun:      true,
	}

	if err := Run(ctx, cfg, opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	pid, iid := firstIssueIDs(t, cfg.DBPath)
	resumeOpts := ResumeOptions{
		ProjectName: "foo",
		IssueID:     iid,
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

	resultPath := storage.ResultPath(pid, iid, "step-1")
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
	issue, err := sqlite.NewIssueRepo(db).Get(iid)
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

	project, err := eng.projects.GetByName("foo")
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	issue, err := eng.issues.Create(project.ID, "add auth", "step-1", `["researcher","planner","implementer"]`)
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

	resultPath := storage.ResultPath(project.ID, issue.ID, "step-1")
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

	// Pipeline should have continued through step-2 and step-3.
	implResultPath := storage.ResultPath(project.ID, issue.ID, "step-3")
	implResult, err := readResult(ctx, eng.store, implResultPath)
	if err != nil {
		t.Fatalf("read step-3 result: %v", err)
	}
	if implResult.Status != "done" {
		t.Fatalf("step-3 result status = %q, want done", implResult.Status)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
