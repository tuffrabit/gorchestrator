package orchestrator

import (
	"log"
	"sync"
)

// inferenceBreaker is the exclusive-mode circuit breaker for inference-side
// failures (model load/unload). When tripped, daemon workers stop claiming
// new issues until a human decides on the issue that tripped it. The latch is
// in-memory: a daemon restart clears it (RecoverAll keeps the failed issue
// visible, so nothing is lost).
type inferenceBreaker struct {
	mu      sync.Mutex
	tripped bool
	issueID int64
	reason  string
}

// Trip latches the breaker for issueID. The first trip wins: calls while
// already tripped are no-ops, so the trip is logged exactly once per
// transition. Returns true when this call tripped the breaker.
func (b *inferenceBreaker) Trip(issueID int64, reason string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tripped {
		return false
	}
	b.tripped = true
	b.issueID = issueID
	b.reason = reason
	log.Printf("inference breaker tripped: issue %d: %s", issueID, reason)
	return true
}

// Active reports whether the breaker is tripped, and if so by which issue
// and for what reason.
func (b *inferenceBreaker) Active() (tripped bool, issueID int64, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped, b.issueID, b.reason
}

// Clear releases the breaker, but only when the human decision targets the
// issue that tripped it. Returns true when the breaker was cleared.
func (b *inferenceBreaker) Clear(issueID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.tripped || b.issueID != issueID {
		return false
	}
	b.tripped = false
	b.issueID = 0
	b.reason = ""
	log.Printf("inference breaker cleared by decision on issue %d", issueID)
	return true
}
