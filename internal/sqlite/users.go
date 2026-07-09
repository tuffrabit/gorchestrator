package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// Role values for users.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// User represents a user row.
type User struct {
	ID           int64
	Email        string
	DisplayName  string
	Role         string
	OIDCSubject  sql.NullString
	PasswordHash sql.NullString
	CreatedAt    string
	LastLoginAt  sql.NullString
}

// UserRepo provides user persistence.
type UserRepo struct {
	db *sql.DB
}

// NewUserRepo creates a new user repository.
func NewUserRepo(db *sql.DB) *UserRepo {
	return &UserRepo{db: db}
}

// Create inserts a user and returns it.
func (r *UserRepo) Create(email, displayName, role string, oidcSubject, passwordHash *string) (*User, error) {
	if role == "" {
		role = RoleMember
	}
	var sub, hash any
	if oidcSubject != nil {
		sub = *oidcSubject
	}
	if passwordHash != nil {
		hash = *passwordHash
	}
	res, err := r.db.Exec(`
		INSERT INTO users (email, display_name, role, oidc_subject, password_hash)
		VALUES (?, ?, ?, ?, ?)`,
		email, displayName, role, sub, hash,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return r.Get(id)
}

// Get fetches a user by id.
func (r *UserRepo) Get(id int64) (*User, error) {
	row := r.db.QueryRow(`
		SELECT id, email, display_name, role, oidc_subject, password_hash, created_at, last_login_at
		FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// GetByEmail fetches a user by email.
func (r *UserRepo) GetByEmail(email string) (*User, error) {
	row := r.db.QueryRow(`
		SELECT id, email, display_name, role, oidc_subject, password_hash, created_at, last_login_at
		FROM users WHERE email = ?`, email)
	return scanUser(row)
}

// GetByOIDCSubject fetches a user by OIDC subject.
func (r *UserRepo) GetByOIDCSubject(subject string) (*User, error) {
	row := r.db.QueryRow(`
		SELECT id, email, display_name, role, oidc_subject, password_hash, created_at, last_login_at
		FROM users WHERE oidc_subject = ?`, subject)
	return scanUser(row)
}

// ListAdmins returns users with the admin role.
func (r *UserRepo) ListAdmins() ([]*User, error) {
	rows, err := r.db.Query(`
		SELECT id, email, display_name, role, oidc_subject, password_hash, created_at, last_login_at
		FROM users WHERE role = ?
		ORDER BY id ASC`, RoleAdmin)
	if err != nil {
		return nil, fmt.Errorf("list admins: %w", err)
	}
	defer rows.Close()
	return scanUsers(rows)
}

// TouchLogin updates last_login_at.
func (r *UserRepo) TouchLogin(id int64) error {
	_, err := r.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// UpdateRole sets a user's role.
func (r *UserRepo) UpdateRole(id int64, role string) error {
	_, err := r.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	return err
}

// UpdatePasswordHash sets a local password hash.
func (r *UserRepo) UpdatePasswordHash(id int64, hash string) error {
	_, err := r.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// UpsertOIDC creates or updates a user from an OIDC login.
// Bootstrap admin emails become/stay admin; new unknown users default to member.
func (r *UserRepo) UpsertOIDC(email, displayName, subject string, bootstrapAdminEmails []string) (*User, error) {
	u, err := r.GetByOIDCSubject(subject)
	if err != nil {
		return nil, err
	}
	if u == nil {
		u, err = r.GetByEmail(email)
		if err != nil {
			return nil, err
		}
	}
	role := RoleMember
	for _, e := range bootstrapAdminEmails {
		if e == email {
			role = RoleAdmin
			break
		}
	}
	if u == nil {
		return r.Create(email, displayName, role, &subject, nil)
	}
	if u.Role != RoleAdmin && role == RoleAdmin {
		if err := r.UpdateRole(u.ID, RoleAdmin); err != nil {
			return nil, err
		}
	}
	if !u.OIDCSubject.Valid || u.OIDCSubject.String != subject {
		_, err = r.db.Exec(`UPDATE users SET oidc_subject = ?, display_name = ? WHERE id = ?`,
			subject, displayName, u.ID)
		if err != nil {
			return nil, err
		}
	} else if displayName != "" && displayName != u.DisplayName {
		_, err = r.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, displayName, u.ID)
		if err != nil {
			return nil, err
		}
	}
	return r.Get(u.ID)
}

func scanUser(row *sql.Row) (*User, error) {
	u := &User{}
	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.OIDCSubject, &u.PasswordHash, &u.CreatedAt, &u.LastLoginAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

func scanUsers(rows *sql.Rows) ([]*User, error) {
	var out []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.OIDCSubject, &u.PasswordHash, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
