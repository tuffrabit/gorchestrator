package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// ChatThread represents a chat_threads row. One thread per
// (user, project, agent_type, flavor) combination.
type ChatThread struct {
	ID        int64
	UserID    int64
	ProjectID int64
	AgentType string
	Flavor    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChatMessage represents a chat_messages row.
type ChatMessage struct {
	ID        int64
	ThreadID  int64
	Role      string
	Content   string
	ToolName  string
	Status    string
	CreatedAt time.Time
}

// ChatRepo provides chat thread and message persistence.
type ChatRepo struct {
	db *sql.DB
}

// NewChatRepo creates a new chat repository.
func NewChatRepo(db *sql.DB) *ChatRepo {
	return &ChatRepo{db: db}
}

// chatThreadColumns is the canonical SELECT list for a chat_threads row.
const chatThreadColumns = `id, user_id, project_id, agent_type, flavor, created_at, updated_at`

// chatMessageColumns is the canonical SELECT list for a chat_messages row.
const chatMessageColumns = `id, thread_id, role, content, tool_name, status, created_at`

// GetOrCreateThread returns the thread for the given (user, project, agent_type,
// flavor), inserting it on first use.
func (r *ChatRepo) GetOrCreateThread(userID, projectID int64, agentType, flavor string) (*ChatThread, error) {
	_, err := r.db.Exec(`
		INSERT INTO chat_threads (user_id, project_id, agent_type, flavor)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, project_id, agent_type, flavor) DO NOTHING`,
		userID, projectID, agentType, flavor)
	if err != nil {
		return nil, fmt.Errorf("insert chat thread: %w", err)
	}
	t, err := r.FindThread(userID, projectID, agentType, flavor)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("chat thread missing after insert")
	}
	return t, nil
}

// GetThread fetches a thread by id. Returns nil, nil when missing.
func (r *ChatRepo) GetThread(id int64) (*ChatThread, error) {
	row := r.db.QueryRow(`SELECT `+chatThreadColumns+` FROM chat_threads WHERE id = ?`, id)
	return scanChatThread(row)
}

// FindThread fetches a thread by its natural key. Returns nil, nil when missing.
func (r *ChatRepo) FindThread(userID, projectID int64, agentType, flavor string) (*ChatThread, error) {
	row := r.db.QueryRow(`
		SELECT `+chatThreadColumns+` FROM chat_threads
		WHERE user_id = ? AND project_id = ? AND agent_type = ? AND flavor = ?`,
		userID, projectID, agentType, flavor)
	return scanChatThread(row)
}

// ListMessages returns the thread's messages ordered by id ASC.
func (r *ChatRepo) ListMessages(threadID int64) ([]*ChatMessage, error) {
	rows, err := r.db.Query(`
		SELECT `+chatMessageColumns+` FROM chat_messages
		WHERE thread_id = ?
		ORDER BY id ASC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("list chat messages: %w", err)
	}
	defer rows.Close()
	return scanChatMessages(rows)
}

// AddMessage inserts a message into the thread and returns its id.
func (r *ChatRepo) AddMessage(threadID int64, role, content, toolName, status string) (int64, error) {
	res, err := r.db.Exec(`
		INSERT INTO chat_messages (thread_id, role, content, tool_name, status)
		VALUES (?, ?, ?, ?, ?)`,
		threadID, role, content, toolName, status)
	if err != nil {
		return 0, fmt.Errorf("insert chat message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

// DeleteMessage removes a message by id.
func (r *ChatRepo) DeleteMessage(id int64) error {
	_, err := r.db.Exec(`DELETE FROM chat_messages WHERE id = ?`, id)
	return err
}

// SetMessageResult updates a message's content and status.
func (r *ChatRepo) SetMessageResult(id int64, content, status string) error {
	_, err := r.db.Exec(`
		UPDATE chat_messages SET content = ?, status = ? WHERE id = ?`,
		content, status, id)
	return err
}

// TouchThread bumps the thread's updated_at.
func (r *ChatRepo) TouchThread(threadID int64) error {
	_, err := r.db.Exec(`UPDATE chat_threads SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, threadID)
	return err
}

func scanChatThread(row *sql.Row) (*ChatThread, error) {
	t := &ChatThread{}
	if err := row.Scan(&t.ID, &t.UserID, &t.ProjectID, &t.AgentType, &t.Flavor, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return t, nil
}

func scanChatMessages(rows *sql.Rows) ([]*ChatMessage, error) {
	var out []*ChatMessage
	for rows.Next() {
		m := &ChatMessage{}
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.ToolName, &m.Status, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
