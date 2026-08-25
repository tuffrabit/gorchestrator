package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// TestScopeHoldRetryFeedbackReachesResearch covers the gate-retry feedback fix:
// Decide on a cleared scope hold writes attempts/1/feedback.md and removes
// result.json, so the retry starts at attempt 1 where buildRetryContext never
// looks. The feedback must be injected into the first run's input. The dryrun
// write_output call echoes the first 200 prompt chars into output.md, which
// makes the prompt observable.
func TestScopeHoldRetryFeedbackReachesResearch(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(testConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "rewrite the whole thing",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("status = %q, want waiting_human (scope hold)", issue.Status)
	}

	if err := eng.Decide(ctx, DecideOptions{
		IssueID:   issue.ID,
		Decision:  "retry",
		Feedback:  "FEEDBACK-MARKER: keep it to the auth module",
		DecidedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue after retry: %v", err)
	}

	data, err := eng.store.Read(ctx, storage.AttemptOutputPath(issue.ProjectID, issue.ID, "research", 1))
	if err != nil {
		t.Fatalf("read research output: %v", err)
	}
	if !strings.Contains(string(data), "FEEDBACK-MARKER") {
		t.Fatalf("gate feedback missing from research input:\n%s", data)
	}

	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("final status = %q, want done", issue.Status)
	}
}

// TestEffortHoldRetryCompletes exercises the same Decide path on the effort
// hold (implementation attempt 1 feedback) and verifies the pipeline runs
// through to done. (Prompt content is not observable for the implementer via
// dryrun; injection is asserted by the scope-hold test above.)
func TestEffortHoldRetryCompletes(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(testConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Ship feature [effort:high]",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusWaitingHuman {
		t.Fatalf("status = %q, want waiting_human (effort hold)", issue.Status)
	}

	if err := eng.Decide(ctx, DecideOptions{
		IssueID:   issue.ID,
		Decision:  "retry",
		Feedback:  "use the existing session store",
		DecidedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	// Feedback must be parked at implementation attempt 1 for the next run.
	if exists, _ := eng.store.Exists(ctx, storage.FeedbackPath(issue.ProjectID, issue.ID, "implementation", 1)); !exists {
		t.Fatal("feedback.md missing at implementation attempt 1")
	}

	if err := eng.ProcessIssue(ctx, issue.ID); err != nil {
		t.Fatalf("ProcessIssue after retry: %v", err)
	}
	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("final status = %q, want done", issue.Status)
	}
}
