package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initBareRemote(t *testing.T) (remote string, seedCommit string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	work := filepath.Join(root, "seed")
	if err := os.MkdirAll(work, 0o755); err != nil {
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
	run(work, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "-A")
	run(work, "commit", "-m", "initial")

	remote = filepath.Join(root, "remote.git")
	run(root, "clone", "--bare", work, remote)
	return remote, work
}

func TestManager_WorktreeLifecycle(t *testing.T) {
	remote, _ := initBareRemote(t)
	storageRoot := t.TempDir()
	m := &Manager{StorageRoot: storageRoot}
	cfg := Config{
		RepoURL:    remote,
		BaseBranch: "main",
	}
	ctx := context.Background()
	const projectID int64 = 1

	if err := m.EnsureCache(ctx, projectID, cfg); err != nil {
		t.Fatalf("EnsureCache: %v", err)
	}
	// Second call fetches.
	if err := m.EnsureCache(ctx, projectID, cfg); err != nil {
		t.Fatalf("EnsureCache fetch: %v", err)
	}

	source := filepath.Join(storageRoot, "projects", "1", "issues", "1", "source")
	if err := m.CreateSourceWorktree(ctx, projectID, source, cfg); err != nil {
		t.Fatalf("CreateSourceWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "README.md")); err != nil {
		t.Fatalf("source missing README: %v", err)
	}

	ws := filepath.Join(storageRoot, "projects", "1", "issues", "1", "implementation", "workspace")
	branch := BranchName(1, 42)
	if err := m.CreateImplementerWorktree(ctx, projectID, ws, branch, cfg); err != nil {
		t.Fatalf("CreateImplementerWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := CommitMessage("add feature", 1, 42)
	created, err := m.CommitAll(ctx, ws, msg, "", "")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !created {
		t.Fatal("expected commit to be created")
	}
	// Clean commit is no-op.
	created, err = m.CommitAll(ctx, ws, msg, "", "")
	if err != nil {
		t.Fatalf("CommitAll clean: %v", err)
	}
	if created {
		t.Fatal("expected no commit on clean tree")
	}

	// Second run id gets a distinct branch/worktree after remove+recreate.
	ws2 := filepath.Join(storageRoot, "projects", "1", "issues", "1", "implementation", "workspace")
	branch2 := BranchName(1, 43)
	if err := m.CreateImplementerWorktree(ctx, projectID, ws2, branch2, cfg); err != nil {
		t.Fatalf("second worktree: %v", err)
	}
	out, err := exec.Command("git", "-C", ws2, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != branch2 {
		t.Fatalf("branch = %q want %q", strings.TrimSpace(string(out)), branch2)
	}
}

func TestBranchNameAndMessage(t *testing.T) {
	if got := BranchName(3, 9); got != "ai-implementer/3-9" {
		t.Fatalf("BranchName: %q", got)
	}
	msg := CommitMessage("fix auth", 3, 9)
	if !strings.Contains(msg, "Issue: #3") || !strings.Contains(msg, "Run: 9") {
		t.Fatalf("message: %q", msg)
	}
}

func TestConfigValidate(t *testing.T) {
	c := &Config{}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for empty config")
	}
	c.RepoURL = "x"
	c.BaseBranch = "main"
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
