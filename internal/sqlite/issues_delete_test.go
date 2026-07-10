package sqlite

import (
	"path/filepath"
	"testing"
)

func TestIssueRepo_DeleteCascade(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	projects := NewProjectRepo(db)
	issues := NewIssueRepo(db)
	runs := NewRunRepo(db)
	decisions := NewDecisionRepo(db)
	notifs := NewNotificationRepo(db)

	p, err := projects.GetOrCreate("acme")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := issues.CreateQueued(p.ID, "delete me", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Create(issue.ID, "researcher", "dryrun", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := decisions.Create(issue.ID, "research"); err != nil {
		t.Fatal(err)
	}
	if _, err := notifs.Create(&issue.ID, "human_gate", "a@b.c", "subj", "body"); err != nil {
		t.Fatal(err)
	}

	if err := issues.Delete(issue.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := issues.Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("issue row still present")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs WHERE issue_id = ?`, issue.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("runs left = %d", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions WHERE issue_id = ?`, issue.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("decisions left = %d", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE issue_id = ?`, issue.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("notifications left = %d", n)
	}

	if err := issues.Delete(issue.ID); err == nil {
		t.Fatal("expected ErrNoRows on second delete")
	}
}
