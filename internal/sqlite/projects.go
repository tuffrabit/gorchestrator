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

// Create inserts a project with empty config and returns it.
func (r *ProjectRepo) Create(name string) (*Project, error) {
	return r.CreateWithConfig(name, "{}")
}

// CreateWithConfig inserts a project with the given config_json and returns it.
func (r *ProjectRepo) CreateWithConfig(name, configJSON string) (*Project, error) {
	if configJSON == "" {
		configJSON = "{}"
	}
	res, err := r.db.Exec(`INSERT INTO projects (name, config_json) VALUES (?, ?)`, name, configJSON)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return r.Get(id)
}

// UpdateConfigJSON replaces the project's config_json blob.
func (r *ProjectRepo) UpdateConfigJSON(id int64, configJSON string) error {
	if configJSON == "" {
		configJSON = "{}"
	}
	_, err := r.db.Exec(`UPDATE projects SET config_json = ? WHERE id = ?`, configJSON, id)
	if err != nil {
		return fmt.Errorf("update project config: %w", err)
	}
	return nil
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
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// List returns all projects ordered by name.
func (r *ProjectRepo) List() ([]*Project, error) {
	rows, err := r.db.Query(`SELECT id, name, config_json, created_at FROM projects ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var out []*Project
	for rows.Next() {
		p := &Project{}
		if err := rows.Scan(&p.ID, &p.Name, &p.ConfigJSON, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
