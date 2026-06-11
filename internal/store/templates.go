package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ProjectTemplate is a user-saved project preset. Only metadata lives in the DB;
// the scaffold files live on disk under DataDir/project-templates/{id}/.
type ProjectTemplate struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ServiceBlock is a user-defined builder block — a single compose service
// fragment stored inline.
type ServiceBlock struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	Service     string    `json:"service"`
	ServiceYAML string    `json:"serviceYaml"`
	Volumes     []string  `json:"volumes"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

// --- project templates -------------------------------------------------------

func (s *Store) ListProjectTemplates(ctx context.Context) ([]ProjectTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, slug, description, created_by, created_at
		FROM project_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectTemplate
	for rows.Next() {
		t, err := scanProjectTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) ProjectTemplateByID(ctx context.Context, id int64) (*ProjectTemplate, error) {
	return scanProjectTemplate(s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, description, created_by, created_at
		FROM project_templates WHERE id = ?`, id))
}

func (s *Store) ProjectTemplateBySlug(ctx context.Context, slug string) (*ProjectTemplate, error) {
	return scanProjectTemplate(s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, description, created_by, created_at
		FROM project_templates WHERE slug = ?`, slug))
}

func (s *Store) CreateProjectTemplate(ctx context.Context, t *ProjectTemplate) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO project_templates (name, slug, description, created_by, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		t.Name, t.Slug, t.Description, t.CreatedBy, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteProjectTemplate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_templates WHERE id = ?`, id)
	return err
}

func scanProjectTemplate(r scanner) (*ProjectTemplate, error) {
	var t ProjectTemplate
	var created string
	err := r.Scan(&t.ID, &t.Name, &t.Slug, &t.Description, &t.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &t, nil
}

// --- service blocks ----------------------------------------------------------

func (s *Store) ListServiceBlocks(ctx context.Context) ([]ServiceBlock, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, slug, description, service, service_yaml, volumes, created_by, created_at
		FROM service_blocks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceBlock
	for rows.Next() {
		b, err := scanServiceBlock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Store) ServiceBlockBySlug(ctx context.Context, slug string) (*ServiceBlock, error) {
	return scanServiceBlock(s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, description, service, service_yaml, volumes, created_by, created_at
		FROM service_blocks WHERE slug = ?`, slug))
}

func (s *Store) ServiceBlockByID(ctx context.Context, id int64) (*ServiceBlock, error) {
	return scanServiceBlock(s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, description, service, service_yaml, volumes, created_by, created_at
		FROM service_blocks WHERE id = ?`, id))
}

func (s *Store) CreateServiceBlock(ctx context.Context, b *ServiceBlock) (int64, error) {
	vols, _ := json.Marshal(b.Volumes)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO service_blocks (name, slug, description, service, service_yaml, volumes, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.Name, b.Slug, b.Description, b.Service, b.ServiceYAML, string(vols), b.CreatedBy,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteServiceBlock(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM service_blocks WHERE id = ?`, id)
	return err
}

func scanServiceBlock(r scanner) (*ServiceBlock, error) {
	var b ServiceBlock
	var volumes, created string
	err := r.Scan(&b.ID, &b.Name, &b.Slug, &b.Description, &b.Service, &b.ServiceYAML, &volumes, &b.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if volumes != "" {
		_ = json.Unmarshal([]byte(volumes), &b.Volumes)
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &b, nil
}
