package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// recordController counts unloads and captures the context state observed at
// UnloadAll entry, to prove the release path uses a live (non-cancelled)
// context after a user stop.
type recordController struct {
	mu           sync.Mutex
	loads        int
	unloads      int
	unloadCtxErr error
}

func (c *recordController) EnsureLoaded(ctx context.Context, model string, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads++
	return nil
}

func (c *recordController) RunningModels(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (c *recordController) UnloadAll(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unloads++
	c.unloadCtxErr = ctx.Err()
	return nil
}

func (c *recordController) snapshot() (loads, unloads int, unloadCtxErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads, c.unloads, c.unloadCtxErr
}

func TestStopIssue_QueuedIssue(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "foo",
		IssueTitle:  "queued stop",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.StopIssue(ctx, issue.ID); err != nil {
		t.Fatalf("StopIssue: %v", err)
	}

	got, err := eng.issues.Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != sqlite.StatusStopped {
		t.Fatalf("status = %q, want stopped", got.Status)
	}

	nonTerminal, err := eng.issues.ListNonTerminal()
	if err != nil {
		t.Fatal(err)
	}
	for _, nt := range nonTerminal {
		if nt.ID == issue.ID {
			t.Fatal("stopped issue must be terminal (ListNonTerminal)")
		}
	}
}

func TestStopIssue_TerminalIssueErrors(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "foo",
		IssueTitle:  "already done",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.issues.UpdateStatus(issue.ID, sqlite.StatusDone, "implementation"); err != nil {
		t.Fatal(err)
	}

	if err := eng.StopIssue(ctx, issue.ID); err == nil {
		t.Fatal("expected error stopping a done issue")
	}
	if _, err := eng.issues.Get(issue.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStopIssue_RunningPipeline(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	cfg.Inference = config.InferenceConfig{
		Type:    "llama-swap",
		BaseURL: "http://stop-test.invalid",
		Mode:    "exclusive",
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	ctrl := &recordController{}
	eng.controller = ctrl

	// [block] makes the dry-run model block until the run context is cancelled.
	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "foo",
		IssueTitle:  "[block] stop me",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- eng.ProcessIssue(ctx, issue.ID) }()

	// Wait for the run to register (agent is then blocked on the model call).
	deadline := time.Now().Add(2 * time.Second)
	for !eng.runActive(issue.ID) {
		if time.Now().After(deadline) {
			t.Fatal("run never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := eng.StopIssue(ctx, issue.ID); err != nil {
		t.Fatalf("StopIssue: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ProcessIssue after stop: %v (a user stop is not an error)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pipeline did not unwind after stop")
	}

	got, err := eng.issues.Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != sqlite.StatusStopped {
		t.Fatalf("status = %q, want stopped", got.Status)
	}

	loads, unloads, unloadCtxErr := ctrl.snapshot()
	if loads == 0 {
		t.Fatal("expected EnsureLoaded to be called")
	}
	if unloads == 0 {
		t.Fatal("expected UnloadAll after stop (exclusive mode must not pin the model)")
	}
	if unloadCtxErr != nil {
		t.Fatalf("UnloadAll received a cancelled context: %v", unloadCtxErr)
	}

	if tripped, _ := eng.InferenceBreakerTripped(); tripped {
		t.Fatal("breaker tripped by a user stop")
	}

	// Stopped issue is deletable.
	if err := eng.DeleteIssue(ctx, issue.ID); err != nil {
		t.Fatalf("DeleteIssue after stop: %v", err)
	}
}

func TestDecide_RetryFromStopped(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "foo",
		IssueTitle:  "stop then retry",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.StopIssue(ctx, issue.ID); err != nil {
		t.Fatalf("StopIssue: %v", err)
	}

	if err := eng.Decide(ctx, DecideOptions{
		IssueID:  issue.ID,
		Decision: "retry",
		Feedback: "try again",
	}); err != nil {
		t.Fatalf("Decide retry from stopped: %v", err)
	}

	got, err := eng.issues.Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != sqlite.StatusQueued {
		t.Fatalf("status = %q, want queued after retry", got.Status)
	}
}

func TestDeleteIssue_ClearsInferenceBreaker(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "delproj",
		IssueTitle:  "tripped then deleted",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !eng.breaker.Trip(issue.ID, "test trip") {
		t.Fatal("breaker did not trip")
	}
	if err := eng.DeleteIssue(ctx, issue.ID); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	if tripped, id := eng.InferenceBreakerTripped(); tripped {
		t.Fatalf("breaker still tripped by deleted issue %d", id)
	}
}

func TestDeleteIssue_RefusesActiveRun(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	eng, err := NewEngine(testConfig(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "delproj",
		IssueTitle:  "active then deleted",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, unregister := eng.registerRun(issue.ID, ctx)
	err = eng.DeleteIssue(ctx, issue.ID)
	if !errors.Is(err, ErrIssueActive) {
		t.Fatalf("DeleteIssue with active run = %v, want ErrIssueActive", err)
	}
	unregister()

	if err := eng.DeleteIssue(ctx, issue.ID); err != nil {
		t.Fatalf("DeleteIssue after unregister: %v", err)
	}
}
