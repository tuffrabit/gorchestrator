// Package git implements orchestrator-managed git workspace lifecycle.
// Agents never invoke git; only the orchestrator does.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AuthType is how the host authenticates to the remote.
type AuthType string

const (
	AuthSSHKey AuthType = "ssh_key"
	AuthToken  AuthType = "token"
	AuthGHCLI  AuthType = "gh_cli"
)

// Config is project-level git configuration (from projects.config_json).
type Config struct {
	RepoURL     string   `json:"repo_url"`
	BaseBranch  string   `json:"base_branch"`
	Push        bool     `json:"push"`
	CreatePR    bool     `json:"create_pr"`
	AuthorName  string   `json:"author_name"`
	AuthorEmail string   `json:"author_email"`
	Auth        AuthConfig `json:"auth"`
}

// AuthConfig selects credential mode (credentials live in the environment).
type AuthConfig struct {
	Type       AuthType `json:"type"`
	SSHKeyPath string   `json:"ssh_key_path"`
	TokenEnv   string   `json:"token_env"`
	GHProfile  string   `json:"gh_profile"`
}

// DefaultAuthorName/Email used when config omits them.
const (
	DefaultAuthorName  = "gorchestrator"
	DefaultAuthorEmail = "gorchestrator@localhost"
)

// Enabled reports whether git workspace management should be used.
func (c *Config) Enabled() bool {
	return c != nil && strings.TrimSpace(c.RepoURL) != ""
}

// Validate checks prerequisites for using this config.
func (c *Config) Validate() error {
	if c == nil || c.RepoURL == "" {
		return fmt.Errorf("git.repo_url is required")
	}
	if c.BaseBranch == "" {
		return fmt.Errorf("git.base_branch is required")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not found on PATH: %w", err)
	}
	if c.CreatePR || c.Auth.Type == AuthGHCLI {
		if _, err := exec.LookPath("gh"); err != nil {
			return fmt.Errorf("gh not found on PATH (required for create_pr / gh_cli auth): %w", err)
		}
	}
	return nil
}

// BranchName returns the standard implementer branch for a run.
func BranchName(issueID, runID int64) string {
	return fmt.Sprintf("ai-implementer/%d-%d", issueID, runID)
}

// CommitMessage builds the structured single-commit message for a run.
func CommitMessage(title string, issueID, runID int64) string {
	return fmt.Sprintf("ai-implementer: %s\n\nIssue: #%d\nRun: %d\nAgent: implementer\n", title, issueID, runID)
}

// Manager runs git operations against a storage root (host filesystem).
type Manager struct {
	StorageRoot string
}

// CachePath returns the bare clone path for a project.
func (m *Manager) CachePath(projectID int64) string {
	return filepath.Join(m.StorageRoot, "repos", fmt.Sprintf("%d.git", projectID))
}

// EnsureCache clones (bare) or fetches the project repository.
func (m *Manager) EnsureCache(ctx context.Context, projectID int64, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cache := m.CachePath(projectID)
	if _, err := os.Stat(cache); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
			return fmt.Errorf("mkdir repos: %w", err)
		}
		if err := m.run(ctx, "", "clone", "--bare", cfg.RepoURL, cache); err != nil {
			return fmt.Errorf("git clone --bare: %w", err)
		}
		return nil
	}
	if err := m.run(ctx, cache, "fetch", "--all", "--prune"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	return nil
}

// CreateSourceWorktree checks out base_branch into absPath (issue source/).
func (m *Manager) CreateSourceWorktree(ctx context.Context, projectID int64, absPath string, cfg Config) error {
	cache := m.CachePath(projectID)
	if err := m.removeWorktreeIfPresent(ctx, cache, absPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	// Detach or branch checkout of base; use local tracking ref after fetch.
	ref := "refs/heads/" + cfg.BaseBranch
	// Prefer origin/base if heads not present on bare.
	if err := m.run(ctx, cache, "show-ref", "--verify", "--quiet", ref); err != nil {
		ref = "origin/" + cfg.BaseBranch
	}
	if err := m.run(ctx, cache, "worktree", "add", "--force", "--detach", absPath, ref); err != nil {
		return fmt.Errorf("worktree add source: %w", err)
	}
	return nil
}

// CreateImplementerWorktree creates a new branch and worktree at absPath.
func (m *Manager) CreateImplementerWorktree(ctx context.Context, projectID int64, absPath, branch string, cfg Config) error {
	cache := m.CachePath(projectID)
	if err := m.removeWorktreeIfPresent(ctx, cache, absPath); err != nil {
		return err
	}
	// Delete leftover branch name if present (retry after failed run).
	_ = m.run(ctx, cache, "branch", "-D", branch)

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	startRef := cfg.BaseBranch
	if err := m.run(ctx, cache, "show-ref", "--verify", "--quiet", "refs/heads/"+cfg.BaseBranch); err != nil {
		startRef = "origin/" + cfg.BaseBranch
	}
	if err := m.run(ctx, cache, "worktree", "add", "-b", branch, absPath, startRef); err != nil {
		return fmt.Errorf("worktree add implementer: %w", err)
	}
	return nil
}

// CommitAll stages all changes and creates a single commit. No-op if clean.
// Returns true if a commit was created.
func (m *Manager) CommitAll(ctx context.Context, workspaceAbs, message, authorName, authorEmail string) (bool, error) {
	if authorName == "" {
		authorName = DefaultAuthorName
	}
	if authorEmail == "" {
		authorEmail = DefaultAuthorEmail
	}
	if err := m.run(ctx, workspaceAbs, "add", "-A"); err != nil {
		return false, fmt.Errorf("git add: %w", err)
	}
	// Check for staged changes.
	err := m.run(ctx, workspaceAbs, "diff", "--cached", "--quiet")
	if err == nil {
		return false, nil // clean
	}
	// non-zero exit means there are diffs
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
	)
	if err := m.runEnv(ctx, workspaceAbs, env, "commit", "-m", message); err != nil {
		return false, fmt.Errorf("git commit: %w", err)
	}
	return true, nil
}

// Push pushes the branch to origin.
func (m *Manager) Push(ctx context.Context, workspaceAbs, branch string) error {
	if err := m.run(ctx, workspaceAbs, "push", "-u", "origin", branch); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

// CreatePR opens a pull request with gh (optional).
func (m *Manager) CreatePR(ctx context.Context, workspaceAbs, baseBranch, title, body string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh not on PATH: %w", err)
	}
	args := []string{"pr", "create", "--base", baseBranch, "--title", title, "--body", body}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = workspaceAbs
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh pr create: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (m *Manager) removeWorktreeIfPresent(ctx context.Context, cache, absPath string) error {
	if _, err := os.Stat(absPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Prefer git worktree remove; fall back to rm -rf.
	if err := m.run(ctx, cache, "worktree", "remove", "--force", absPath); err != nil {
		if rmErr := os.RemoveAll(absPath); rmErr != nil {
			return fmt.Errorf("remove worktree %s: git: %v; rm: %w", absPath, err, rmErr)
		}
	}
	_ = m.run(ctx, cache, "worktree", "prune")
	return nil
}

func (m *Manager) run(ctx context.Context, dir string, args ...string) error {
	return m.runEnv(ctx, dir, nil, args...)
}

func (m *Manager) runEnv(ctx context.Context, dir string, env []string, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound runaway git commands.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// Diff returns unified diff of working tree vs base branch (for drawer).
func (m *Manager) Diff(ctx context.Context, workspaceAbs, baseBranch string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", baseBranch+"...HEAD")
	cmd.Dir = workspaceAbs
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Fallback: unstaged + staged against HEAD
		cmd2 := exec.CommandContext(ctx, "git", "diff", "HEAD")
		cmd2.Dir = workspaceAbs
		var out2 bytes.Buffer
		cmd2.Stdout = &out2
		if err2 := cmd2.Run(); err2 != nil {
			return "", fmt.Errorf("git diff: %w: %s", err, stderr.String())
		}
		return out2.String(), nil
	}
	return stdout.String(), nil
}
