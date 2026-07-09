package sqlite

import (
	"database/sql"
	"fmt"
)

// Decision represents a human adjudication decision row.
type Decision struct {
	ID         int64
	IssueID    int64
	Phase      string
	RequestedAt string
	DecidedAt  sql.NullString
	Decision   sql.NullString
	Feedback   sql.NullString
	DecidedBy  sql.NullString
}

// DecisionRepo provides decision persistence.
type DecisionRepo struct {
	db *sql.DB
}

// NewDecisionRepo creates a new decision repository.
func NewDecisionRepo(db *sql.DB) *DecisionRepo {
	return &DecisionRepo{db: db}
}

// Create inserts a pending decision for a phase and returns it.
func (r *DecisionRepo) Create(issueID int64, phase string) (*Decision, error) {
	res, err := r.db.Exec(
		`INSERT INTO decisions (issue_id, phase) VALUES (?, ?)`,
		issueID, phase,
	)
	if err != nil {
		return nil, fmt.Errorf("insert decision: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return r.Get(id)
}

// Get fetches a decision by id.
func (r *DecisionRepo) Get(id int64) (*Decision, error) {
	row := r.db.QueryRow(`
		SELECT id, issue_id, phase, requested_at, decided_at, decision, feedback, decided_by
		FROM decisions WHERE id = ?`, id)
	d := &Decision{}
	if err := row.Scan(&d.ID, &d.IssueID, &d.Phase, &d.RequestedAt, &d.DecidedAt, &d.Decision, &d.Feedback, &d.DecidedBy); err != nil {
		return nil, err
	}
	return d, nil
}

// Record updates the latest pending decision for an issue/phase with the human verdict.
// If no pending row exists (e.g. retrying a failed phase that never opened a human gate),
// inserts a completed decision row so the audit trail still captures the intervention.
func (r *DecisionRepo) Record(issueID int64, phase, decision, feedback, decidedBy string) error {
	// SQLite does not support ORDER BY in UPDATE. Find the latest pending row first.
	var id int64
	err := r.db.QueryRow(`
		SELECT id FROM decisions
		WHERE issue_id = ? AND phase = ? AND decided_at IS NULL
		ORDER BY requested_at DESC
		LIMIT 1`, issueID, phase).Scan(&id)
	if err == sql.ErrNoRows {
		_, err = r.db.Exec(`
			INSERT INTO decisions (issue_id, phase, decided_at, decision, feedback, decided_by)
			VALUES (?, ?, CURRENT_TIMESTAMP, ?, ?, ?)`,
			issueID, phase, decision, feedback, decidedBy,
		)
		if err != nil {
			return fmt.Errorf("insert decision: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("find pending decision: %w", err)
	}

	_, err = r.db.Exec(`
		UPDATE decisions
		SET decided_at = CURRENT_TIMESTAMP, decision = ?, feedback = ?, decided_by = ?
		WHERE id = ?`,
		decision, feedback, decidedBy, id,
	)
	if err != nil {
		return fmt.Errorf("record decision: %w", err)
	}
	return nil
}

// PendingForIssue returns all pending decisions for an issue.
func (r *DecisionRepo) PendingForIssue(issueID int64) ([]*Decision, error) {
	rows, err := r.db.Query(`
		SELECT id, issue_id, phase, requested_at, decided_at, decision, feedback, decided_by
		FROM decisions
		WHERE issue_id = ? AND decided_at IS NULL
		ORDER BY requested_at DESC`, issueID)
	if err != nil {
		return nil, fmt.Errorf("query pending decisions: %w", err)
	}
	defer rows.Close()

	var out []*Decision
	for rows.Next() {
		d := &Decision{}
		if err := rows.Scan(&d.ID, &d.IssueID, &d.Phase, &d.RequestedAt, &d.DecidedAt, &d.Decision, &d.Feedback, &d.DecidedBy); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
