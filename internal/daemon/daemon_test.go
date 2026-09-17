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

func TestDaemon_InferenceBreakerBlocksClaiming(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Server.MaxConcurrentIssues = 1
	// Exclusive-mode inference pointed at a dead server: the first issue's
	// model load fails fast (connection refused) and trips the breaker.
	cfg.Inference = config.InferenceConfig{
		Type:    "llama-swap",
		BaseURL: "http://127.0.0.1:1",
		Mode:    "exclusive",
	}
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

	first, err := eng.SubmitIssue(ctx, orchestrator.RunOptions{
		ProjectName: "acme",
		IssueTitle:  "trips the breaker",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue first: %v", err)
	}

	// Wait for the worker to fail the issue on the dead inference server and
	// trip the breaker.
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, _ := eng.Issues().Get(first.ID)
		tripped, tripIssue := eng.InferenceBreakerTripped()
		if got != nil && got.Status == sqlite.StatusFailed && tripped && tripIssue == first.ID {
			break
		}
		if time.Now().After(deadline) {
			d.Shutdown(2 * time.Second)
			t.Fatalf("timeout waiting for breaker trip (issue status=%v tripped=%v)", got.Status, tripped)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// While tripped, new queued work must not be claimed.
	second, err := eng.SubmitIssue(ctx, orchestrator.RunOptions{
		ProjectName: "acme",
		IssueTitle:  "blocked by breaker",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue second: %v", err)
	}
	time.Sleep(time.Second)
	got, err := eng.Issues().Get(second.ID)
	if err != nil || got == nil {
		t.Fatalf("get second issue: %v", err)
	}
	if got.Status != sqlite.StatusQueued {
		t.Fatalf("second issue status = %q, want queued while breaker tripped", got.Status)
	}

	// A human decision on the tripped issue clears the breaker; the worker
	// resumes claiming and picks up the queued issue (it also fails against
	// the dead server, re-tripping the breaker — that is expected here).
	if err := eng.Decide(ctx, orchestrator.DecideOptions{
		IssueID:  first.ID,
		Decision: "fail",
		Feedback: "investigated",
	}); err != nil {
		t.Fatalf("Decide on tripped issue: %v", err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		got, _ := eng.Issues().Get(second.ID)
		if got != nil && got.Status == sqlite.StatusFailed {
			break
		}
		if time.Now().After(deadline) {
			d.Shutdown(2 * time.Second)
			t.Fatalf("timeout waiting for second issue to be claimed after Decide (status=%v)", got.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.Shutdown(2 * time.Second)
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
