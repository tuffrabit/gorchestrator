package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// TestScopeHoldRetryFeedbackReachesStep1 covers the gate-retry feedback fix:
// Decide on a cleared scope hold writes attempts/1/feedback.md and removes
// result.json, so the retry starts at attempt 1 where buildRetryContext never
// looks. The feedback must be injected into the first run's input. The dryrun
// write_output call echoes the first 200 prompt chars into output.md, which
// makes the prompt observable.
func TestScopeHoldRetryFeedbackReachesStep1(t *testing.T) {
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

	data, err := eng.store.Read(ctx, storage.AttemptOutputPath(issue.ProjectID, issue.ID, "step-1", 1))
	if err != nil {
		t.Fatalf("read step-1 output: %v", err)
	}
	if !strings.Contains(string(data), "FEEDBACK-MARKER") {
		t.Fatalf("gate feedback missing from step-1 input:\n%s", data)
	}

	issue, _ = eng.Issues().Get(issue.ID)
	if issue.Status != sqlite.StatusDone {
		t.Fatalf("final status = %q, want done", issue.Status)
	}
}
