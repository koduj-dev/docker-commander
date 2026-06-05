package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Project is a managed compose project: a folder under the data dir holding a
// compose file plus sidecar config/script files, deployed via the docker
// compose CLI. The folder is keyed by the numeric ID (derived at runtime, not
// stored) so renames never move files. Slug is the compose project name.
type Project struct {
	ID          int64
	Name        string
	Slug        string
	ComposeFile string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CreateProject inserts a project and returns its ID. A slug collision yields
// ErrDuplicate.
func (s *Store) CreateProject(ctx context.Context, p *Project) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (name, slug, compose_file, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.Slug, orDefault(p.ComposeFile, "compose.yml"), p.CreatedBy, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

// ListProjects returns all projects ordered by name.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, slug, compose_file, created_by, created_at, updated_at
		FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProjectRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ProjectByID looks up a project by primary key.
func (s *Store) ProjectByID(ctx context.Context, id int64) (*Project, error) {
	return scanProjectRow(s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, compose_file, created_by, created_at, updated_at
		FROM projects WHERE id = ?`, id))
}

// UpdateProjectName changes the display name (the slug stays immutable).
func (s *Store) UpdateProjectName(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET name = ?, updated_at = ? WHERE id = ?`,
		name, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// TouchProject bumps updated_at (called when a file changes).
func (s *Store) TouchProject(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// DeleteProject removes the project row (the caller removes the folder).
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	return err
}

func scanProjectRow(row scanner) (*Project, error) {
	var p Project
	var createdAt, updatedAt string
	err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.ComposeFile, &p.CreatedBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &p, nil
}
