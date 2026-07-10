package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestDeleteIssue_RemovesDBAndFilesystem(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "delproj",
		IssueTitle:  "to delete",
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed artifacts under the issue directory.
	dir := storage.IssueDir(issue.ProjectID, issue.ID)
	if err := eng.Store().Write(ctx, filepath.ToSlash(filepath.Join(dir, "research", "output.md")), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.runs.Create(issue.ID, "researcher", "dryrun", "done"); err != nil {
		t.Fatal(err)
	}

	ch := eng.Subscribe(ctx, EventFilter{IssueID: issue.ID})
	if err := eng.DeleteIssue(ctx, issue.ID); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	// Wait briefly for bus event.
	select {
	case ev := <-ch:
		if ev.Type != EventIssueDeleted {
			t.Fatalf("event type = %q, want %s", ev.Type, EventIssueDeleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for issue_deleted event")
	}

	got, err := eng.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("issue still visible via GetIssue")
	}
	exists, err := eng.Store().Exists(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("issue storage dir still exists")
	}

	if err := eng.DeleteIssue(ctx, issue.ID); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("second delete: %v, want ErrIssueNotFound", err)
	}
}
