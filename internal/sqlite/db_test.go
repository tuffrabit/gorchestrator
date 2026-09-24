package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpen_MigrationsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gorchestrator.db")

	for i := 0; i < 3; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != len(migrations) {
		t.Fatalf("migrations = %d, want %d", count, len(migrations))
	}
}

func TestOpen_MigrationAppliesToExistingDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gorchestrator.db")

	// Bring the DB up to the latest schema, then roll back migration 13
	// so it looks like a pre-migration-13 database file.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 13`); err != nil {
		t.Fatalf("remove migration record: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE issues DROP COLUMN pipeline_json`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening the existing file must apply migration 13.
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var colCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('issues') WHERE name = 'pipeline_json'`).Scan(&colCount); err != nil {
		t.Fatalf("check column: %v", err)
	}
	if colCount != 1 {
		t.Fatalf("pipeline_json column present %d times, want 1", colCount)
	}

	// New inserts pick up the '[]' default.
	if _, err := db.Exec(`INSERT INTO projects (name) VALUES ('acme')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var pid int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name = 'acme'`).Scan(&pid); err != nil {
		t.Fatalf("project id: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO issues (project_id, title) VALUES (?, 'x')`, pid); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	var pipeline string
	if err := db.QueryRow(`SELECT pipeline_json FROM issues`).Scan(&pipeline); err != nil {
		t.Fatalf("read pipeline_json: %v", err)
	}
	if pipeline != "[]" {
		t.Fatalf("pipeline_json default = %q, want \"[]\"", pipeline)
	}
}

func TestOpen_PragmasActive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gorchestrator.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}

	var busy int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busy)
	}

	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
}
