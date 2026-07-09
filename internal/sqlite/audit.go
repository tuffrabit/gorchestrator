package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// AuditEntry represents an audit_log row.
type AuditEntry struct {
	ID          int64
	UserID      sql.NullInt64
	Action      string
	TargetType  string
	TargetID    string
	DetailsJSON string
	CreatedAt   string
}

// AuditRepo provides audit log persistence.
type AuditRepo struct {
	db *sql.DB
}

// NewAuditRepo creates a new audit repository.
func NewAuditRepo(db *sql.DB) *AuditRepo {
	return &AuditRepo{db: db}
}

// Record inserts an audit log entry. userID may be nil for system/cli actions.
func (r *AuditRepo) Record(userID *int64, action, targetType, targetID string, details any) error {
	detailsJSON := "{}"
	if details != nil {
		data, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
		detailsJSON = string(data)
	}
	var uid any
	if userID != nil {
		uid = *userID
	}
	_, err := r.db.Exec(`
		INSERT INTO audit_log (user_id, action, target_type, target_id, details_json)
		VALUES (?, ?, ?, ?, ?)`,
		uid, action, targetType, targetID, detailsJSON,
	)
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

// ListRecent returns the most recent audit entries.
func (r *AuditRepo) ListRecent(limit int) ([]*AuditEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Query(`
		SELECT id, user_id, action, target_type, target_id, details_json, created_at
		FROM audit_log
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()

	var out []*AuditEntry
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.UserID, &e.Action, &e.TargetType, &e.TargetID, &e.DetailsJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
