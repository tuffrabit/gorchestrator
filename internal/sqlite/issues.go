package sqlite

import (
	"database/sql"
	"encoding/json"
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
	StatusStopped      = "stopped"
)

// Issue represents an issue row.
type Issue struct {
	ID                  int64
	ProjectID           int64
	Title               string
	Description         string // optional; mirrored to issue.md on submit
	Status              string
	CurrentPhase        string
	DryRun              bool
	Source              string // manual | webhook | github | jira | ...
	ExternalID          string
	AgentFlavorsJSON    string // frozen cast: {"researcher":"cheap",...}
	PipelineJSON        string // frozen agent flow: ["researcher","coder"]; empty/[] = legacy issue
	BudgetOverridesJSON string // provider → absolute session ceiling: {"openai":200000}
	DependsOnJSON       string // issue IDs that must be done first: [12,14]
	CreatedAt           string
	UpdatedAt           string
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
// currentPhase is the first phase/step key ("step-1" for flow issues,
// "research" for legacy-shaped test helpers); pipelineJSON is the frozen
// agent flow ("[]" for legacy-shaped rows); the column defaults are never
// relied on.
func (r *IssueRepo) Create(projectID int64, title, currentPhase, pipelineJSON string) (*Issue, error) {
	return r.CreateWithStatus(projectID, title, StatusInProgress, currentPhase, pipelineJSON, "{}", "[]", false, "manual", "")
}

// CreateQueued inserts an issue with status queued for the daemon worker pool.
func (r *IssueRepo) CreateQueued(projectID int64, title, currentPhase, pipelineJSON string, dryRun bool) (*Issue, error) {
	return r.CreateWithStatus(projectID, title, StatusQueued, currentPhase, pipelineJSON, "{}", "[]", dryRun, "manual", "")
}

// CreateQueuedFrom creates a queued issue with provenance (webhook/github/jira),
// the frozen agent flow, and an optional dependency list (JSON array of issue
// IDs, "" = none). agentFlavorsJSON is kept for legacy-shaped rows; pass "{}"
// for flow issues.
func (r *IssueRepo) CreateQueuedFrom(projectID int64, title, currentPhase, pipelineJSON, agentFlavorsJSON, dependsOnJSON string, dryRun bool, source, externalID string) (*Issue, error) {
	if source == "" {
		source = "manual"
	}
	return r.CreateWithStatus(projectID, title, StatusQueued, currentPhase, pipelineJSON, agentFlavorsJSON, dependsOnJSON, dryRun, source, externalID)
}

// CreateWithStatus inserts an issue with the given status and dry-run flag.
func (r *IssueRepo) CreateWithStatus(projectID int64, title, status, currentPhase, pipelineJSON, agentFlavorsJSON, dependsOnJSON string, dryRun bool, source, externalID string) (*Issue, error) {
	if currentPhase == "" {
		currentPhase = "research"
	}
	if pipelineJSON == "" {
		pipelineJSON = "[]"
	}
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
	if dependsOnJSON == "" {
		dependsOnJSON = "[]"
	}
	res, err := r.db.Exec(
		`INSERT INTO issues (project_id, title, description, status, current_phase, dry_run, source, external_id, agent_flavors_json, pipeline_json, depends_on_json) VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, title, status, currentPhase, dry, source, externalID, agentFlavorsJSON, pipelineJSON, dependsOnJSON,
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

// issueColumns is the canonical SELECT list for an issue row.
const issueColumns = `id, project_id, title, description, status, current_phase, dry_run, source, external_id, agent_flavors_json, pipeline_json, budget_overrides_json, depends_on_json, created_at, updated_at`

// Get fetches an issue by id.
func (r *IssueRepo) Get(id int64) (*Issue, error) {
	row := r.db.QueryRow(`SELECT `+issueColumns+` FROM issues WHERE id = ?`, id)
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

// SetBudgetOverrides stores absolute per-provider session ceilings as JSON.
func (r *IssueRepo) SetBudgetOverrides(id int64, overridesJSON string) error {
	if overridesJSON == "" {
		overridesJSON = "{}"
	}
	_, err := r.db.Exec(
		`UPDATE issues SET budget_overrides_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		overridesJSON, id,
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

// claimCandidateLimit bounds how many queued rows ClaimQueued inspects per attempt.
const claimCandidateLimit = 50

// ClaimQueued atomically claims the oldest queued issue with satisfied
// dependencies by setting it in_progress. Queued issues whose depends_on IDs
// are not all done are skipped (failed/cancelled deps do NOT unblock); FIFO
// order is preserved among the eligible. Returns nil, nil when the queue is
// empty or every candidate is blocked.
func (r *IssueRepo) ClaimQueued() (*Issue, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`
		SELECT id, depends_on_json FROM issues
		WHERE status = ?
		ORDER BY created_at ASC, id ASC
		LIMIT ?`, StatusQueued, claimCandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("select queued: %w", err)
	}
	type candidate struct {
		id        int64
		dependsOn string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.dependsOn); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan queued: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("select queued: %w", err)
	}
	rows.Close()
	if len(candidates) == 0 {
		return nil, nil
	}

	for _, c := range candidates {
		if deps := ParseDependsOn(c.dependsOn); len(deps) > 0 {
			statuses, err := statusesByID(tx, deps)
			if err != nil {
				return nil, fmt.Errorf("check deps for issue %d: %w", c.id, err)
			}
			if len(unsatisfiedDeps(deps, statuses)) > 0 {
				continue // blocked; try the next candidate
			}
		}

		res, err := tx.Exec(`
			UPDATE issues
			SET status = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND status = ?`,
			StatusInProgress, c.id, StatusQueued,
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
		return r.Get(c.id)
	}
	// Every queued candidate is blocked by unsatisfied dependencies.
	return nil, nil
}

// UnsatisfiedDeps returns the subset of deps whose issues are missing or not
// done. Read-side helper for the "blocked" view of queued issues.
func (r *IssueRepo) UnsatisfiedDeps(deps []int64) ([]int64, error) {
	if len(deps) == 0 {
		return nil, nil
	}
	statuses, err := statusesByID(r.db, deps)
	if err != nil {
		return nil, err
	}
	return unsatisfiedDeps(deps, statuses), nil
}

// ParseDependsOn decodes the depends_on_json column (JSON array of issue IDs).
// Empty/invalid values yield no dependencies, mirroring the cast JSON helpers.
func ParseDependsOn(raw string) []int64 {
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []int64
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

type idQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// statusesByID fetches current statuses for the given issue IDs.
func statusesByID(q idQuerier, ids []int64) (map[int64]string, error) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := q.Query(
		`SELECT id, status FROM issues WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("select dep statuses: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		out[id] = status
	}
	return out, rows.Err()
}

// unsatisfiedDeps returns deps that are missing or whose status is not done.
func unsatisfiedDeps(deps []int64, statuses map[int64]string) []int64 {
	var out []int64
	for _, id := range deps {
		if statuses[id] != StatusDone {
			out = append(out, id)
		}
	}
	return out
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
		SELECT %s
		FROM issues
		%s
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?`, issueColumns, where)
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer rows.Close()
	return scanIssues(rows)
}

// ListNonTerminal returns issues that are not in a terminal status.
func (r *IssueRepo) ListNonTerminal() ([]*Issue, error) {
	rows, err := r.db.Query(`SELECT `+issueColumns+`
		FROM issues
		WHERE status NOT IN (?, ?, ?, ?)
		ORDER BY id ASC`,
		StatusDone, StatusFailed, StatusCancelled, StatusStopped,
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
	if err := row.Scan(&i.ID, &i.ProjectID, &i.Title, &i.Description, &i.Status, &i.CurrentPhase, &dry, &i.Source, &i.ExternalID, &i.AgentFlavorsJSON, &i.PipelineJSON, &i.BudgetOverridesJSON, &i.DependsOnJSON, &i.CreatedAt, &i.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	i.DryRun = dry != 0
	i.normalizeJSON()
	return i, nil
}

func scanIssues(rows *sql.Rows) ([]*Issue, error) {
	var out []*Issue
	for rows.Next() {
		i := &Issue{}
		var dry int
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.Title, &i.Description, &i.Status, &i.CurrentPhase, &dry, &i.Source, &i.ExternalID, &i.AgentFlavorsJSON, &i.PipelineJSON, &i.BudgetOverridesJSON, &i.DependsOnJSON, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		i.DryRun = dry != 0
		i.normalizeJSON()
		out = append(out, i)
	}
	return out, rows.Err()
}

func (i *Issue) normalizeJSON() {
	if i.AgentFlavorsJSON == "" {
		i.AgentFlavorsJSON = "{}"
	}
	if i.PipelineJSON == "" {
		i.PipelineJSON = "[]"
	}
	if i.BudgetOverridesJSON == "" {
		i.BudgetOverridesJSON = "{}"
	}
	if i.DependsOnJSON == "" {
		i.DependsOnJSON = "[]"
	}
}
