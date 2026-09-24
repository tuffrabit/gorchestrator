package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// defaultTestFlowJSON is the frozen pipeline_json for the standard 3-agent
// fixture flow (researcher → planner → implementer).
const defaultTestFlowJSON = `["researcher","planner","implementer"]`

// newManualEngine boots an Engine with the given config and creates a queued
// project+issue pair for direct engine-level tests.
func newManualEngine(t *testing.T, cfg *config.Config) (*Engine, *sqlite.Project, *sqlite.Issue) {
	t.Helper()
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	project, err := eng.projects.GetByName("foo")
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	issue, err := eng.issues.Create(project.ID, "add auth", "step-1", defaultTestFlowJSON)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return eng, project, issue
}

// drainEvents collects whatever is already buffered on an event channel.
func drainEvents(ch <-chan Event) []Event {
	var out []Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// blockingController models an inference controller whose EnsureLoaded blocks
// until release is closed (or the context is cancelled).
type blockingController struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce syncOnce
}

type syncOnce struct{ done bool }

func (o *syncOnce) Do(f func()) {
	if o.done {
		return
	}
	o.done = true
	f()
}

func (c *blockingController) EnsureLoaded(ctx context.Context, model string, timeout time.Duration) error {
	c.enteredOnce.Do(func() { close(c.entered) })
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *blockingController) RunningModels(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (c *blockingController) UnloadAll(ctx context.Context, keep ...string) error {
	return nil
}

// spyController counts lifecycle calls.
type spyController struct{ calls int }

func (s *spyController) EnsureLoaded(ctx context.Context, model string, timeout time.Duration) error {
	s.calls++
	return nil
}

func (s *spyController) RunningModels(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (s *spyController) UnloadAll(ctx context.Context, keep ...string) error {
	s.calls++
	return nil
}
