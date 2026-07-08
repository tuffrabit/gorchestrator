package sqlite

import (
	"database/sql"
	"fmt"
)

// Issue represents an issue row.
type Issue struct {
	ID           int64
	ProjectID    int64
	Title        string
	Status       string
	CurrentPhase string
	CreatedAt    string
	UpdatedAt    string
}

// IssueRepo provides issue persistence.
type IssueRepo struct {
	db *sql.DB
}

// NewIssueRepo creates a new issue repository.
func NewIssueRepo(db *sql.DB) *IssueRepo {
	return &IssueRepo{db: db}
}

// Create inserts an issue and returns it.
func (r *IssueRepo) Create(projectID int64, title string) (*Issue, error) {
	res, err := r.db.Exec(
		`INSERT INTO issues (project_id, title, status, current_phase) VALUES (?, ?, 'in_progress', 'research')`,
		projectID, title,
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
	row := r.db.QueryRow(`SELECT id, project_id, title, status, current_phase, created_at, updated_at FROM issues WHERE id = ?`, id)
	i := &Issue{}
	if err := row.Scan(&i.ID, &i.ProjectID, &i.Title, &i.Status, &i.CurrentPhase, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, err
	}
	return i, nil
}

// UpdateStatus updates the status and current phase of an issue.
func (r *IssueRepo) UpdateStatus(id int64, status, phase string) error {
	_, err := r.db.Exec(
		`UPDATE issues SET status = ?, current_phase = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, phase, id,
	)
	return err
}
