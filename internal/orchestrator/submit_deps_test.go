package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

func TestSubmitIssue_RejectsMissingDependency(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(testConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	_, err = eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "orphan dep",
		DryRun:      true,
		DependsOn:   []int64{999},
	})
	if err == nil || !strings.Contains(err.Error(), "depends_on issue 999 does not exist") {
		t.Fatalf("SubmitIssue err = %v, want missing depends_on error", err)
	}

	_, err = eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "bad dep id",
		DryRun:      true,
		DependsOn:   []int64{-1},
	})
	if err == nil || !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("SubmitIssue err = %v, want invalid depends_on error", err)
	}
}

func TestSubmitIssue_StoresDependenciesAndBlocks(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(testConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	blocker, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "blocker",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue blocker: %v", err)
	}
	dependent, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "dependent",
		DryRun:      true,
		DependsOn:   []int64{blocker.ID},
	})
	if err != nil {
		t.Fatalf("SubmitIssue dependent: %v", err)
	}
	if dependent.DependsOnJSON != fmt.Sprintf("[%d]", blocker.ID) {
		t.Fatalf("DependsOnJSON = %q, want [%d]", dependent.DependsOnJSON, blocker.ID)
	}

	// Queued blocker is not done → dependent surfaces as blocked.
	view, err := eng.GetIssue(ctx, dependent.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(view.BlockedBy) != 1 || view.BlockedBy[0] != blocker.ID {
		t.Fatalf("BlockedBy = %v, want [%d]", view.BlockedBy, blocker.ID)
	}

	// Daemon claim skips the dependent and picks the older blocker first.
	claimed, err := eng.Issues().ClaimQueued()
	if err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}
	if claimed == nil || claimed.ID != blocker.ID {
		t.Fatalf("claimed = %+v, want blocker %d", claimed, blocker.ID)
	}
	if claimed, err := eng.Issues().ClaimQueued(); err != nil || claimed != nil {
		t.Fatalf("second claim = %+v, %v; want nil (dependent blocked)", claimed, err)
	}

	// Once the blocker reaches done, the dependent is claimable and unblocked.
	if err := eng.Issues().UpdateStatus(blocker.ID, sqlite.StatusDone, "implementation"); err != nil {
		t.Fatal(err)
	}
	claimed, err = eng.Issues().ClaimQueued()
	if err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}
	if claimed == nil || claimed.ID != dependent.ID {
		t.Fatalf("claimed = %+v, want dependent %d", claimed, dependent.ID)
	}
	view, err = eng.GetIssue(ctx, dependent.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(view.BlockedBy) != 0 {
		t.Fatalf("BlockedBy = %v, want empty after claim", view.BlockedBy)
	}
}
