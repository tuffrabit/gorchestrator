package orchestrator

import (
	"context"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

func TestBreaker_TripClearSemantics(t *testing.T) {
	b := &inferenceBreaker{}

	if tripped, _, _ := b.Active(); tripped {
		t.Fatal("breaker tripped at construction")
	}
	if !b.Trip(7, "inference error during research: boom") {
		t.Fatal("first Trip did not report a transition")
	}
	// First trip wins: a second trip is a no-op and keeps the original issue.
	if b.Trip(8, "another failure") {
		t.Fatal("second Trip reported a transition while already tripped")
	}
	tripped, issueID, reason := b.Active()
	if !tripped || issueID != 7 || reason == "" {
		t.Fatalf("Active = (%v, %d, %q), want tripped on issue 7", tripped, issueID, reason)
	}

	// Clear on a different issue must not release the breaker.
	if b.Clear(8) {
		t.Fatal("Clear(8) cleared a breaker tripped by issue 7")
	}
	if tripped, _, _ := b.Active(); !tripped {
		t.Fatal("breaker cleared by a decision on the wrong issue")
	}

	if !b.Clear(7) {
		t.Fatal("Clear(7) did not clear the breaker")
	}
	if tripped, _, _ := b.Active(); tripped {
		t.Fatal("breaker still tripped after Clear on the tripped issue")
	}
}

func TestBreaker_DecideOnTrippedIssueClears(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)

	eng, project, issue := newManualEngine(t, cfg)

	other, err := eng.issues.CreateQueued(project.ID, "unrelated queued issue", true)
	if err != nil {
		t.Fatalf("create queued issue: %v", err)
	}

	eng.breaker.Trip(issue.ID, "inference error during research: boom")

	// While tripped, workers see an empty queue.
	if claimed, err := eng.ClaimIssue(); err != nil || claimed != nil {
		t.Fatalf("ClaimIssue while tripped = (%v, %v), want (nil, nil)", claimed, err)
	}

	// A decision on a DIFFERENT issue must not clear the breaker.
	if err := eng.Decide(ctx, DecideOptions{
		IssueID:  other.ID,
		Decision: "fail",
		Feedback: "not this one",
		Force:    true,
	}); err != nil {
		t.Fatalf("Decide on other issue: %v", err)
	}
	if tripped, _ := eng.InferenceBreakerTripped(); !tripped {
		t.Fatal("breaker cleared by a decision on a different issue")
	}

	// A retry decision on the tripped issue clears the breaker; claiming
	// resumes and the retried issue is claimable again.
	if err := eng.issues.UpdateStatus(issue.ID, sqlite.StatusFailed, "research"); err != nil {
		t.Fatalf("mark tripped issue failed: %v", err)
	}
	if err := eng.Decide(ctx, DecideOptions{
		IssueID:  issue.ID,
		Decision: "retry",
		Feedback: "server is back",
	}); err != nil {
		t.Fatalf("Decide retry on tripped issue: %v", err)
	}
	if tripped, _ := eng.InferenceBreakerTripped(); tripped {
		t.Fatal("breaker still tripped after deciding on the tripped issue")
	}
	claimed, err := eng.ClaimIssue()
	if err != nil || claimed == nil {
		t.Fatalf("ClaimIssue after clear = (%v, %v), want a claim", claimed, err)
	}
}
