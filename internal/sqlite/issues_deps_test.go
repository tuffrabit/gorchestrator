package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// testFlowJSON is a valid frozen agent flow used by sqlite tests.
const testFlowJSON = `["researcher","planner","implementer"]`

func depsTestRepo(t *testing.T) (*IssueRepo, *ProjectRepo) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewIssueRepo(db), NewProjectRepo(db)
}

func TestClaimQueued_SkipsBlockedClaimsEligible(t *testing.T) {
	issues, projects := depsTestRepo(t)
	p, err := projects.Create("acme")
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := issues.Create(p.ID, "blocker", "step-1", testFlowJSON) // in_progress
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := issues.CreateQueuedFrom(p.ID, "dependent", "step-1", testFlowJSON, "{}", fmt.Sprintf("[%d]", blocker.ID), false, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	free, err := issues.CreateQueued(p.ID, "free", "step-1", testFlowJSON, false)
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != free.ID {
		t.Fatalf("claimed = %+v, want issue %d (free)", claimed, free.ID)
	}

	// The blocked dependent must not be claimed while the blocker is not done.
	claimed, err = issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("claimed = %+v, want nil (only blocked issue %d remains)", claimed, blocked.ID)
	}
}

func TestClaimQueued_ClaimsAfterDependencyDone(t *testing.T) {
	issues, projects := depsTestRepo(t)
	p, err := projects.Create("acme")
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := issues.Create(p.ID, "blocker", "step-1", testFlowJSON) // in_progress
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := issues.CreateQueuedFrom(p.ID, "dependent", "step-1", testFlowJSON, "{}", fmt.Sprintf("[%d]", blocker.ID), false, "manual", "")
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("claimed = %+v, want nil while blocker in_progress", claimed)
	}

	if err := issues.UpdateStatus(blocker.ID, StatusDone, "implementation"); err != nil {
		t.Fatal(err)
	}
	claimed, err = issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != dependent.ID {
		t.Fatalf("claimed = %+v, want issue %d after blocker done", claimed, dependent.ID)
	}
}

func TestClaimQueued_FIFOPreservedAmongEligible(t *testing.T) {
	issues, projects := depsTestRepo(t)
	p, err := projects.Create("acme")
	if err != nil {
		t.Fatal(err)
	}

	first, err := issues.CreateQueued(p.ID, "first", "step-1", testFlowJSON, false)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := issues.Create(p.ID, "blocker", "step-1", testFlowJSON) // in_progress
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := issues.CreateQueuedFrom(p.ID, "blocked", "step-1", testFlowJSON, "{}", fmt.Sprintf("[%d]", blocker.ID), false, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	third, err := issues.CreateQueued(p.ID, "third", "step-1", testFlowJSON, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []*Issue{first, third} {
		claimed, err := issues.ClaimQueued()
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil || claimed.ID != want.ID {
			t.Fatalf("claimed = %+v, want issue %d (%s)", claimed, want.ID, want.Title)
		}
	}

	// Only the blocked issue remains queued.
	claimed, err := issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("claimed = %+v, want nil (issue %d blocked)", claimed, blocked.ID)
	}

	if err := issues.UpdateStatus(blocker.ID, StatusDone, "implementation"); err != nil {
		t.Fatal(err)
	}
	claimed, err = issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != blocked.ID {
		t.Fatalf("claimed = %+v, want issue %d after unblock", claimed, blocked.ID)
	}
}

func TestClaimQueued_FailedDependencyStaysBlocked(t *testing.T) {
	issues, projects := depsTestRepo(t)
	p, err := projects.Create("acme")
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := issues.CreateQueued(p.ID, "blocker", "step-1", testFlowJSON, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := issues.UpdateStatus(blocker.ID, StatusFailed, "research"); err != nil {
		t.Fatal(err)
	}
	if _, err := issues.CreateQueuedFrom(p.ID, "dependent", "step-1", testFlowJSON, "{}", fmt.Sprintf("[%d]", blocker.ID), false, "manual", ""); err != nil {
		t.Fatal(err)
	}

	claimed, err := issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("claimed = %+v, want nil (dep on failed issue does not unblock)", claimed)
	}
}

func TestMigration_DependsOnUpgradeFromOldDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// Simulate a pre-v11 database: apply every migration before issue_depends_on.
	cutoff := len(migrations)
	for i, m := range migrations {
		if m.name == "issue_depends_on" {
			cutoff = i
			break
		}
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:cutoff] {
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
			t.Fatal(err)
		}
	}
	// Row written before the column existed.
	if _, err := raw.Exec(`INSERT INTO issues (project_id, title, status) VALUES (1, 'old row', 'queued')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open upgraded db: %v", err)
	}
	defer db.Close()
	issues := NewIssueRepo(db)

	old, err := issues.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if old == nil {
		t.Fatal("old row missing after upgrade")
	}
	if old.DependsOnJSON != "[]" {
		t.Fatalf("DependsOnJSON = %q, want []", old.DependsOnJSON)
	}

	// A row with an empty depends_on_json also behaves as no-deps.
	if _, err := db.Exec(`UPDATE issues SET depends_on_json = '' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	old, err = issues.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if old.DependsOnJSON != "[]" {
		t.Fatalf("DependsOnJSON after empty = %q, want []", old.DependsOnJSON)
	}

	claimed, err := issues.ClaimQueued()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != old.ID {
		t.Fatalf("claimed = %+v, want old row %d (no deps)", claimed, old.ID)
	}
}

func TestParseDependsOn(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", "not-json"} {
		if got := ParseDependsOn(raw); len(got) != 0 {
			t.Fatalf("ParseDependsOn(%q) = %v, want empty", raw, got)
		}
	}
	got := ParseDependsOn("[12,14]")
	if len(got) != 2 || got[0] != 12 || got[1] != 14 {
		t.Fatalf("ParseDependsOn([12,14]) = %v", got)
	}
}
