package sqlite

import (
	"database/sql"
	"fmt"
)

// Run represents a run row.
type Run struct {
	ID          int64
	IssueID     int64
	AgentType   string
	Model       string
	Status      string
	TokensUsed  int
	DurationMs  int
	LoopCount   int
	WorkspaceID string
	BranchName  string
	CreatedAt   string
}

// RunRepo provides run persistence.
type RunRepo struct {
	db *sql.DB
}

// NewRunRepo creates a new run repository.
func NewRunRepo(db *sql.DB) *RunRepo {
	return &RunRepo{db: db}
}

// Create inserts a run and returns it.
func (r *RunRepo) Create(issueID int64, agentType, model, status string) (*Run, error) {
	res, err := r.db.Exec(
		`INSERT INTO runs (issue_id, agent_type, model, status) VALUES (?, ?, ?, ?)`,
		issueID, agentType, model, status,
	)
	if err != nil {
		return nil, fmt.Errorf("insert run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return r.Get(id)
}

// Get fetches a run by id.
func (r *RunRepo) Get(id int64) (*Run, error) {
	row := r.db.QueryRow(`SELECT id, issue_id, agent_type, model, status, tokens_used, duration_ms, loop_count, workspace_id, branch_name, created_at FROM runs WHERE id = ?`, id)
	run := &Run{}
	if err := row.Scan(&run.ID, &run.IssueID, &run.AgentType, &run.Model, &run.Status, &run.TokensUsed, &run.DurationMs, &run.LoopCount, &run.WorkspaceID, &run.BranchName, &run.CreatedAt); err != nil {
		return nil, err
	}
	return run, nil
}

// UpdateStatus updates the status and metrics of a run.
func (r *RunRepo) UpdateStatus(id int64, status string, tokensUsed, durationMs, loopCount int) error {
	_, err := r.db.Exec(
		`UPDATE runs SET status = ?, tokens_used = ?, duration_ms = ?, loop_count = ? WHERE id = ?`,
		status, tokensUsed, durationMs, loopCount, id,
	)
	return err
}

// SetWorkspace records the workspace path key and git branch for a run.
func (r *RunRepo) SetWorkspace(id int64, workspaceID, branchName string) error {
	_, err := r.db.Exec(
		`UPDATE runs SET workspace_id = ?, branch_name = ? WHERE id = ?`,
		workspaceID, branchName, id,
	)
	return err
}

// TokenTotalForIssue returns the sum of tokens_used across all runs for an issue.
func (r *RunRepo) TokenTotalForIssue(issueID int64) (int, error) {
	var total sql.NullInt64
	err := r.db.QueryRow(`SELECT SUM(tokens_used) FROM runs WHERE issue_id = ?`, issueID).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return int(total.Int64), nil
}
