package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Session represents a session row.
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt string
	CreatedAt string
}

// SessionRepo provides session persistence.
type SessionRepo struct {
	db *sql.DB
}

// NewSessionRepo creates a new session repository.
func NewSessionRepo(db *sql.DB) *SessionRepo {
	return &SessionRepo{db: db}
}

// Create inserts a new session with a random opaque token.
func (r *SessionRepo) Create(userID int64, ttl time.Duration) (*Session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(ttl).Format(time.RFC3339)
	_, err = r.db.Exec(`
		INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
		id, userID, expires,
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return r.Get(id)
}

// Get fetches a session by id.
func (r *SessionRepo) Get(id string) (*Session, error) {
	row := r.db.QueryRow(`
		SELECT id, user_id, expires_at, created_at FROM sessions WHERE id = ?`, id)
	s := &Session{}
	if err := row.Scan(&s.ID, &s.UserID, &s.ExpiresAt, &s.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return s, nil
}

// Delete removes a session.
func (r *SessionRepo) Delete(id string) error {
	_, err := r.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteExpired removes expired sessions.
func (r *SessionRepo) DeleteExpired() (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IsExpired reports whether the session has passed its expiry.
func (s *Session) IsExpired() bool {
	t, err := time.Parse(time.RFC3339, s.ExpiresAt)
	if err != nil {
		return true
	}
	return time.Now().UTC().After(t)
}

func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
