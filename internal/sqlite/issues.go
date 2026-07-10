package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

// Issue status values used by the daemon queue and CLI.
const (
	StatusQueued       = "queued"
	StatusInProgress   = "in_progress"
	StatusWaitingHuman = "waiting_human"
	StatusDone         = "done"
	StatusFailed       = "failed"
	StatusCancelled    = "cancelled"
)

// Issue represents an issue row.
type Issue struct {
	ID               int64
	ProjectID        int64
	Title            string
	Description      string // optional; mirrored to issue.md on submit
	Status           string
	CurrentPhase     string
	DryRun           bool
	Source           string // manual | webhook | github | jira | ...
	ExternalID       string
	AgentFlavorsJSON string // frozen cast: {"researcher":"cheap",...}
	CreatedAt        string
	UpdatedAt        string
}

// IssueListFilter selects issues for listing.
type IssueListFilter struct {
	ProjectID int64  // 0 = any
	Status    string // empty = any
	Limit     int    // 0 = default 100
	Offset    int
}

// IssueRepo provides issue persistence.
type IssueRepo struct {
	db *sql.DB
}

// NewIssueRepo creates a new issue repository.
func NewIssueRepo(db *sql.DB) *IssueRepo {
	return &IssueRepo{db: db}
}

// Create inserts an issue as in_progress (CLI path) and returns it.
func (r *IssueRepo) Create(projectID int64, title string) (*Issue, error) {
	return r.CreateWithStatus(projectID, title, StatusInProgress, false, "manual", "", "{}")
}

// CreateQueued inserts an issue with status queued for the daemon worker pool.
func (r *IssueRepo) CreateQueued(projectID int64, title string, dryRun bool) (*Issue, error) {
	return r.CreateWithStatus(projectID, title, StatusQueued, dryRun, "manual", "", "{}")
}

// CreateQueuedFrom creates a queued issue with provenance (webhook/github/jira).
func (r *IssueRepo) CreateQueuedFrom(projectID int64, title string, dryRun bool, source, externalID, agentFlavorsJSON string) (*Issue, error) {
	if source == "" {
		source = "manual"
	}
	return r.CreateWithStatus(projectID, title, StatusQueued, dryRun, source, externalID, agentFlavorsJSON)
}

// CreateWithCast inserts an in_progress issue with a frozen agent cast (CLI Run path).
func (r *IssueRepo) CreateWithCast(projectID int64, title, agentFlavorsJSON string) (*Issue, error) {
	return r.CreateWithStatus(projectID, title, StatusInProgress, false, "manual", "", agentFlavorsJSON)
}

// CreateWithStatus inserts an issue with the given status and dry-run flag.
func (r *IssueRepo) CreateWithStatus(projectID int64, title, status string, dryRun bool, source, externalID, agentFlavorsJSON string) (*Issue, error) {
	dry := 0
	if dryRun {
		dry = 1
	}
	if source == "" {
		source = "manual"
	}
	if agentFlavorsJSON == "" {
		agentFlavorsJSON = "{}"
	}
	res, err := r.db.Exec(
		`INSERT INTO issues (project_id, title, description, status, current_phase, dry_run, source, external_id, agent_flavors_json) VALUES (?, ?, '', ?, 'research', ?, ?, ?, ?)`,
		projectID, title, status, dry, source, externalID, agentFlavorsJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("insert issue: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return r.Get(id)
}

// Get fetches an issue by id.
func (r *IssueRepo) Get(id int64) (*Issue, error) {
	row := r.db.QueryRow(`
		SELECT id, project_id, title, description, status, current_phase, dry_run, source, external_id, agent_flavors_json, created_at, updated_at
		FROM issues WHERE id = ?`, id)
	return scanIssue(row)
}

// SetDescription updates the issue description column.
// Callers that also maintain issue.md must write the file in the same flow
// so SQLite and filesystem stay aligned (see Engine.persistIssueContext).
func (r *IssueRepo) SetDescription(id int64, description string) error {
	_, err := r.db.Exec(
		`UPDATE issues SET description = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		description, id,
	)
	return err
}

// UpdateStatus updates the status and current phase of an issue.
func (r *IssueRepo) UpdateStatus(id int64, status, phase string) error {
	_, err := r.db.Exec(
		`UPDATE issues SET status = ?, current_phase = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, phase, id,
	)
	return err
}

// ClaimQueued atomically claims the oldest queued issue by setting it in_progress.
// Returns nil, nil when the queue is empty.
func (r *IssueRepo) ClaimQueued() (*Issue, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRow(`
		SELECT id FROM issues
		WHERE status = ?
		ORDER BY created_at ASC, id ASC
		LIMIT 1`, StatusQueued).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select queued: %w", err)
	}

	res, err := tx.Exec(`
		UPDATE issues
		SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = ?`,
		StatusInProgress, id, StatusQueued,
	)
	if err != nil {
		return nil, fmt.Errorf("claim update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		// Lost the race; treat as empty for this attempt.
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return r.Get(id)
}

// List returns issues matching the filter, newest updated first.
func (r *IssueRepo) List(f IssueListFilter) ([]*Issue, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var conds []string
	var args []any
	if f.ProjectID > 0 {
		conds = append(conds, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, project_id, title, description, status, current_phase, dry_run, source, external_id, agent_flavors_json, created_at, updated_at
		FROM issues
		%s
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?`, where)
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer rows.Close()
	return scanIssues(rows)
}

// ListNonTerminal returns issues that are not in a terminal status.
func (r *IssueRepo) ListNonTerminal() ([]*Issue, error) {
	rows, err := r.db.Query(`
		SELECT id, project_id, title, description, status, current_phase, dry_run, source, external_id, agent_flavors_json, created_at, updated_at
		FROM issues
		WHERE status NOT IN (?, ?, ?)
		ORDER BY id ASC`,
		StatusDone, StatusFailed, StatusCancelled,
	)
	if err != nil {
		return nil, fmt.Errorf("list non-terminal issues: %w", err)
	}
	defer rows.Close()
	return scanIssues(rows)
}

// Requeue sets a claimed/crashed issue back to queued for workers.
func (r *IssueRepo) Requeue(id int64) error {
	_, err := r.db.Exec(`
		UPDATE issues SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		StatusQueued, id,
	)
	return err
}

// Delete hard-deletes an issue and all dependent rows (runs, decisions,
// notifications). Audit log entries are retained (target_id is not a FK).
// Returns sql.ErrNoRows when the issue id does not exist.
func (r *IssueRepo) Delete(id int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete issue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM runs WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete runs: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM decisions WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete decisions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM notifications WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete notifications: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete issue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete issue: %w", err)
	}
	return nil
}

func scanIssue(row *sql.Row) (*Issue, error) {
	i := &Issue{}
	var dry int
	if err := row.Scan(&i.ID, &i.ProjectID, &i.Title, &i.Description, &i.Status, &i.CurrentPhase, &dry, &i.Source, &i.ExternalID, &i.AgentFlavorsJSON, &i.CreatedAt, &i.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	i.DryRun = dry != 0
	if i.AgentFlavorsJSON == "" {
		i.AgentFlavorsJSON = "{}"
	}
	return i, nil
}

func scanIssues(rows *sql.Rows) ([]*Issue, error) {
	var out []*Issue
	for rows.Next() {
		i := &Issue{}
		var dry int
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.Title, &i.Description, &i.Status, &i.CurrentPhase, &dry, &i.Source, &i.ExternalID, &i.AgentFlavorsJSON, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		i.DryRun = dry != 0
		if i.AgentFlavorsJSON == "" {
			i.AgentFlavorsJSON = "{}"
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
