package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

func testConfig(tmp string) *config.Config {
	return &config.Config{
		StorageRoot: tmp,
		DBPath:      filepath.Join(tmp, "gorchestrator.db"),
		DefaultModel: config.ModelConfig{
			Provider:   "dryrun",
			Model:      "dryrun-model",
			Timeout:    "30s",
			TimeoutDur: 30 * time.Second,
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileConfig{MaxBytes: 64 * 1024, MaxLines: 2000},
		},
		Agents: map[string]config.AgentConfig{
			"researcher":  {Adjudicator: "self", MaxAttempts: 1, Loops: 1},
			"planner":     {Adjudicator: "self", MaxAttempts: 1, Loops: 1},
			"implementer": {Adjudicator: "self", MaxAttempts: 1, Loops: 1},
		},
		Projects: map[string]config.ProjectConfig{
			"acme": {},
		},
		Server: config.ServerConfig{
			Listen:              "127.0.0.1:0",
			MaxConcurrentIssues: 2,
			ShutdownTimeout:     "5s",
			ShutdownTimeoutDur:  5 * time.Second,
		},
		Auth: config.AuthConfig{Mode: "disabled", SessionTTLDur: time.Hour},
	}
}

func TestDaemon_WorkersProcessQueue(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := New(eng, cfg)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, err := eng.SubmitIssue(ctx, orchestrator.RunOptions{
			ProjectName: "acme",
			IssueTitle:  "issue",
			DryRun:      true,
		})
		if err != nil {
			t.Fatalf("SubmitIssue: %v", err)
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		views, err := eng.ListIssues(ctx, sqlite.IssueListFilter{Limit: 10})
		if err != nil {
			t.Fatalf("ListIssues: %v", err)
		}
		allDone := len(views) == 3
		for _, v := range views {
			if v.Issue.Status != sqlite.StatusDone {
				allDone = false
				break
			}
		}
		if allDone {
			d.Shutdown(2 * time.Second)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.Shutdown(2 * time.Second)
	t.Fatal("timeout waiting for workers to complete issues")
}

func TestDaemon_ShutdownDoesNotMarkFailed(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Server.MaxConcurrentIssues = 1
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	ctx, cancel := context.WithCancel(context.Background())
	d := New(eng, cfg)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Submit then immediately shut down — status must not become failed.
	_, _ = eng.SubmitIssue(ctx, orchestrator.RunOptions{
		ProjectName: "acme",
		IssueTitle:  "shutdown race",
		DryRun:      true,
	})
	cancel()
	d.Shutdown(2 * time.Second)

	views, err := eng.ListIssues(context.Background(), sqlite.IssueListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, v := range views {
		if v.Issue.Status == sqlite.StatusFailed {
			t.Fatalf("issue marked failed on clean shutdown: %+v", v.Issue)
		}
	}
}
