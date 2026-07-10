package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func initLocalBareRemote(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "hello.go"), []byte("package hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-m", "initial")
	remote := filepath.Join(root, "remote.git")
	run(root, "clone", "--bare", seed, remote)
	return remote
}

func TestGitWorkspace_PipelineDryRun(t *testing.T) {
	remote := initLocalBareRemote(t)
	tmp := t.TempDir()
	cfg := &config.Config{
		StorageRoot: filepath.Join(tmp, "storage"),
		DBPath:      filepath.Join(tmp, "db.sqlite"),
		DefaultModel: config.ModelConfig{
			Provider: "dryrun",
			Model:    "dryrun",
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileConfig{MaxBytes: 65536, MaxLines: 2000},
		},
		Agents: map[string]config.AgentConfig{
			"researcher":  {Adjudicator: "null", MaxAttempts: 1, Loops: 1},
			"planner":     {Adjudicator: "null", MaxAttempts: 1, Loops: 1},
			"implementer": {Adjudicator: "null", MaxAttempts: 1, Loops: 1},
		},
		Projects: map[string]config.ProjectConfig{
			"gitproj": {
				Git: &config.ProjectGitConfig{
					RepoURL:    remote,
					BaseBranch: "main",
					Push:       false,
				},
			},
		},
	}

	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	project, err := eng.projects.GetByName("gitproj")
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := eng.Run(ctx, RunOptions{
		ProjectName: "gitproj",
		IssueTitle:  "add feature",
		DryRun:      true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Source worktree should exist with hello.go
	srcKey := storage.SourcePath(project.ID, 1)
	exists, err := eng.store.Exists(ctx, srcKey+"/hello.go")
	if err != nil || !exists {
		t.Fatalf("source hello.go missing: exists=%v err=%v", exists, err)
	}

	// Workspace should be a git branch with a commit after implementer.
	wsAbs := storage.Abs(cfg.StorageRoot, storage.WorkspacePath(project.ID, 1))
	out, err := exec.Command("git", "-C", wsAbs, "log", "-1", "--pretty=%s").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	// Dry-run implementer may not write files — commit is no-op if clean.
	// Branch should still exist from worktree creation.
	br, err := exec.Command("git", "-C", wsAbs, "branch", "--show-current").Output()
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(br)), "ai-implementer/") {
		t.Fatalf("unexpected branch %q (log subject %q)", strings.TrimSpace(string(br)), strings.TrimSpace(string(out)))
	}

	// runs table should have branch_name for implementer
	runs, err := eng.db.Query(`SELECT branch_name FROM runs WHERE agent_type='implementer'`)
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Close()
	found := false
	for runs.Next() {
		var bn string
		if err := runs.Scan(&bn); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(bn, "ai-implementer/") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected implementer run with branch_name")
	}
}
