package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// Notification status values.
const (
	NotifyPending = "pending"
	NotifySent    = "sent"
	NotifyFailed  = "failed"
)

// Notification represents a notifications row.
type Notification struct {
	ID        int64
	IssueID   sql.NullInt64
	Kind      string
	Recipient string
	Subject   string
	Body      string
	Status    string
	Error     string
	CreatedAt string
	SentAt    sql.NullString
}

// NotificationRepo provides notification persistence.
type NotificationRepo struct {
	db *sql.DB
}

// NewNotificationRepo creates a new notification repository.
func NewNotificationRepo(db *sql.DB) *NotificationRepo {
	return &NotificationRepo{db: db}
}

// Create inserts a pending notification.
func (r *NotificationRepo) Create(issueID *int64, kind, recipient, subject, body string) (*Notification, error) {
	var iid any
	if issueID != nil {
		iid = *issueID
	}
	res, err := r.db.Exec(`
		INSERT INTO notifications (issue_id, kind, recipient, subject, body, status)
		VALUES (?, ?, ?, ?, ?, ?)`,
		iid, kind, recipient, subject, body, NotifyPending,
	)
	if err != nil {
		return nil, fmt.Errorf("insert notification: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return r.Get(id)
}

// Get fetches a notification by id.
func (r *NotificationRepo) Get(id int64) (*Notification, error) {
	row := r.db.QueryRow(`
		SELECT id, issue_id, kind, recipient, subject, body, status, error, created_at, sent_at
		FROM notifications WHERE id = ?`, id)
	n := &Notification{}
	if err := row.Scan(&n.ID, &n.IssueID, &n.Kind, &n.Recipient, &n.Subject, &n.Body, &n.Status, &n.Error, &n.CreatedAt, &n.SentAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return n, nil
}

// MarkSent marks a notification as successfully sent.
func (r *NotificationRepo) MarkSent(id int64) error {
	_, err := r.db.Exec(`
		UPDATE notifications SET status = ?, sent_at = ?, error = '' WHERE id = ?`,
		NotifySent, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// MarkFailed marks a notification as failed with an error message.
func (r *NotificationRepo) MarkFailed(id int64, errMsg string) error {
	_, err := r.db.Exec(`
		UPDATE notifications SET status = ?, error = ? WHERE id = ?`,
		NotifyFailed, errMsg, id,
	)
	return err
}

// ListRecent returns the most recent notifications.
func (r *NotificationRepo) ListRecent(limit int) ([]*Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Query(`
		SELECT id, issue_id, kind, recipient, subject, body, status, error, created_at, sent_at
		FROM notifications
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	return scanNotifications(rows)
}

// ListPending returns pending notifications oldest-first.
func (r *NotificationRepo) ListPending(limit int) ([]*Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Query(`
		SELECT id, issue_id, kind, recipient, subject, body, status, error, created_at, sent_at
		FROM notifications
		WHERE status = ?
		ORDER BY created_at ASC, id ASC
		LIMIT ?`, NotifyPending, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending notifications: %w", err)
	}
	defer rows.Close()
	return scanNotifications(rows)
}

// CountPendingGates returns the count of pending human_gate notifications
// plus issues currently waiting_human (for the dashboard badge).
func (r *NotificationRepo) CountPendingHumanGates() (int, error) {
	var n int
	err := r.db.QueryRow(`
		SELECT COUNT(*) FROM issues WHERE status = ?`, StatusWaitingHuman).Scan(&n)
	return n, err
}

func scanNotifications(rows *sql.Rows) ([]*Notification, error) {
	var out []*Notification
	for rows.Next() {
		n := &Notification{}
		if err := rows.Scan(&n.ID, &n.IssueID, &n.Kind, &n.Recipient, &n.Subject, &n.Body, &n.Status, &n.Error, &n.CreatedAt, &n.SentAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
