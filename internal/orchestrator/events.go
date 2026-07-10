package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Event type constants for the in-process daemon bus.
const (
	EventIssueSubmitted    = "issue_submitted"
	EventIssueStatus       = "issue_status"
	EventIssueDeleted      = "issue_deleted"
	EventPhaseStarted      = "phase_started"
	EventPhaseFinished     = "phase_finished"
	EventDecisionRequested = "decision_requested"
	EventDecisionApplied   = "decision_applied"
	EventRunEvent          = "run_event"
)

// Event is a daemon bus message for SSE and internal subscribers.
type Event struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	IssueID   int64          `json:"issue_id,omitempty"`
	ProjectID int64          `json:"project_id,omitempty"`
	Phase     string         `json:"phase,omitempty"`
	Status    string         `json:"status,omitempty"`
	Message   string         `json:"message,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// EventFilter selects which events a subscriber receives.
type EventFilter struct {
	IssueID int64 // 0 = all issues
}

// EventBus is an in-process pub/sub with drop-on-slow-subscriber policy.
// Slow SSE clients that cannot keep up lose intermediate events and should reconnect.
type EventBus struct {
	mu   sync.RWMutex
	subs map[uint64]*subscription
	next atomic.Uint64
}

type subscription struct {
	id     uint64
	filter EventFilter
	ch     chan Event
}

// NewEventBus creates an empty bus.
func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[uint64]*subscription)}
}

// Publish sends an event to all matching subscribers.
// Non-blocking: full subscriber buffers drop the event for that subscriber.
func (b *EventBus) Publish(ev Event) {
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		if sub.filter.IssueID != 0 && sub.filter.IssueID != ev.IssueID {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// drop-on-slow-subscriber
		}
	}
}

// Subscribe returns a channel of events matching filter.
// Cancel ctx (or call the returned cancel) to unsubscribe and close the channel.
func (b *EventBus) Subscribe(ctx context.Context, filter EventFilter) <-chan Event {
	id := b.next.Add(1)
	ch := make(chan Event, 64)
	sub := &subscription{id: id, filter: filter, ch: ch}

	b.mu.Lock()
	b.subs[id] = sub
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		close(ch)
	}()

	return ch
}

// SubscriberCount returns the number of active subscribers (for tests).
func (b *EventBus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
