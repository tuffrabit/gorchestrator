package notify

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

type captureSink struct {
	mu   sync.Mutex
	msgs []Notification
}

func (c *captureSink) Send(ctx context.Context, n Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, n)
	return nil
}

func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func testDispatcher(t *testing.T) (*Dispatcher, *captureSink, func()) {
	t.Helper()
	tmp := t.TempDir()
	db, err := sqlite.Open(tmp + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	d := NewDispatcher(sqlite.NewNotificationRepo(db), sink)
	return d, sink, func() { _ = db.Close() }
}

func TestEscalator_ConsecutiveFailuresOnce(t *testing.T) {
	d, sink, closeFn := testDispatcher(t)
	defer closeFn()

	cfg := &config.Config{
		Escalation: config.EscalationConfig{
			Rules: []config.EscalationRule{{
				Name:      "three_fails",
				When:      WhenConsecutiveFailures,
				Project:   "acme",
				Threshold: 3,
				Notify:    "console",
			}},
		},
		Auth: config.AuthConfig{BootstrapAdminEmails: []string{"a@b.c"}},
	}
	esc := NewEscalator(cfg, d)
	ctx := context.Background()
	for i := range 5 {
		esc.Observe(ctx, Event{
			When:    WhenPhaseFailed,
			Project: "acme",
			IssueID: 0, // no issues row in this unit test (FK-safe)
			Phase:   "research",
			Message: fmt.Sprintf("boom-%d", i),
		})
	}
	// Only one escalation notification (to console), not 5.
	// Note: each escalation Send is one notification; threshold fires once.
	if sink.count() != 1 {
		t.Fatalf("notifications = %d, want 1 (no storm)", sink.count())
	}
	if len(esc.Recent()) != 1 {
		t.Fatalf("recent = %d", len(esc.Recent()))
	}
}

func TestEscalator_BudgetExceeded(t *testing.T) {
	d, sink, closeFn := testDispatcher(t)
	defer closeFn()
	cfg := &config.Config{
		Escalation: config.EscalationConfig{
			Rules: []config.EscalationRule{{
				Name:      "budget",
				When:      WhenBudgetExceeded,
				Threshold: 1,
				Notify:    "console",
			}},
		},
	}
	esc := NewEscalator(cfg, d)
	ctx := context.Background()
	esc.Observe(ctx, Event{When: WhenBudgetExceeded, Project: "acme", IssueID: 0, Phase: "plan", Message: "budget_exceeded"})
	esc.Observe(ctx, Event{When: WhenBudgetExceeded, Project: "acme", IssueID: 0, Phase: "plan", Message: "budget_exceeded"})
	if sink.count() != 1 {
		t.Fatalf("count = %d, want 1 per issue", sink.count())
	}
}

func TestEscalator_SuccessResetsConsecutive(t *testing.T) {
	d, sink, closeFn := testDispatcher(t)
	defer closeFn()
	cfg := &config.Config{
		Escalation: config.EscalationConfig{
			Rules: []config.EscalationRule{{
				Name:      "two_fails",
				When:      WhenConsecutiveFailures,
				Project:   "*",
				Threshold: 2,
				Notify:    "console",
			}},
		},
	}
	esc := NewEscalator(cfg, d)
	ctx := context.Background()
	esc.Observe(ctx, Event{When: WhenPhaseFailed, Project: "p", IssueID: 0, Message: "a"})
	esc.Observe(ctx, Event{Success: true, Project: "p"})
	esc.Observe(ctx, Event{When: WhenPhaseFailed, Project: "p", IssueID: 0, Message: "b"})
	// After reset, only one failure — should not fire yet.
	if sink.count() != 0 {
		t.Fatalf("count = %d after reset, want 0", sink.count())
	}
	esc.Observe(ctx, Event{When: WhenPhaseFailed, Project: "p", IssueID: 0, Message: "c"})
	if sink.count() != 1 {
		t.Fatalf("count = %d after second failure post-reset, want 1", sink.count())
	}
}

func TestClassifyFailure(t *testing.T) {
	if ClassifyFailure("loop 2: budget_exceeded: spent 10") != WhenBudgetExceeded {
		t.Fatal("budget")
	}
	if ClassifyFailure("run_test: no container runtime (sandbox refused)") != WhenSandboxRefused {
		t.Fatal("sandbox")
	}
	if ClassifyFailure("empty output") != WhenPhaseFailed {
		t.Fatal("phase")
	}
}

func TestNewEscalator_NilWithoutRules(t *testing.T) {
	if NewEscalator(&config.Config{}, nil) != nil {
		t.Fatal("expected nil")
	}
}
