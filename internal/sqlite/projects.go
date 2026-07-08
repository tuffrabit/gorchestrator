package sqlite

import (
	"database/sql"
	"fmt"
)

// Project represents a project row.
type Project struct {
	ID         int64
	Name       string
	ConfigJSON string
	CreatedAt  string
}

// ProjectRepo provides project persistence.
type ProjectRepo struct {
	db *sql.DB
}

// NewProjectRepo creates a new project repository.
func NewProjectRepo(db *sql.DB) *ProjectRepo {
	return &ProjectRepo{db: db}
}

// Create inserts a project and returns it.
func (r *ProjectRepo) Create(name string) (*Project, error) {
	res, err := r.db.Exec(`INSERT INTO projects (name, config_json) VALUES (?, '{}')`, name)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return r.Get(id)
}

// GetByName fetches a project by name.
func (r *ProjectRepo) GetByName(name string) (*Project, error) {
	row := r.db.QueryRow(`SELECT id, name, config_json, created_at FROM projects WHERE name = ?`, name)
	p := &Project{}
	if err := row.Scan(&p.ID, &p.Name, &p.ConfigJSON, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// Get fetches a project by id.
func (r *ProjectRepo) Get(id int64) (*Project, error) {
	row := r.db.QueryRow(`SELECT id, name, config_json, created_at FROM projects WHERE id = ?`, id)
	p := &Project{}
	if err := row.Scan(&p.ID, &p.Name, &p.ConfigJSON, &p.CreatedAt); err != nil {
		return nil, err
	}
	return p, nil
}

// GetOrCreate fetches a project by name or creates it if missing.
func (r *ProjectRepo) GetOrCreate(name string) (*Project, error) {
	p, err := r.GetByName(name)
	if err != nil {
		return nil, err
	}
	if p != nil {
		return p, nil
	}
	return r.Create(name)
}
