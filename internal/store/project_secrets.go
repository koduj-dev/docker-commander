package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ProjectSecret is a project's named secret, metadata only — the value is
// never part of this struct. See ResolveProjectSecretEnv for the one place
// it's decrypted.
type ProjectSecret struct {
	ID        int64
	ProjectID int64
	Name      string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListProjectSecrets returns a project's secrets without their values.
func (s *Store) ListProjectSecrets(ctx context.Context, projectID int64) ([]ProjectSecret, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, name, created_by, created_at, updated_at
		FROM project_secrets WHERE project_id = ? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectSecret
	for rows.Next() {
		var p ProjectSecret
		var created, updated string
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.CreatedBy, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, created)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateProjectSecret stores a new secret, encrypting the value. Returns
// ErrDuplicate if the project already has a secret with this name.
func (s *Store) CreateProjectSecret(ctx context.Context, projectID int64, name, value, createdBy string) (int64, error) {
	if s.cipher == nil {
		return 0, errors.New("store: cipher not configured")
	}
	enc, err := s.cipher.Encrypt(value)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO project_secrets (project_id, name, value_enc, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, name, enc, createdBy, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateProjectSecretValue re-encrypts and replaces a secret's value. The
// name is immutable — delete and recreate to rename. Returns ErrNotFound if
// no such (projectID, name) exists.
func (s *Store) UpdateProjectSecretValue(ctx context.Context, projectID int64, name, value string) error {
	if s.cipher == nil {
		return errors.New("store: cipher not configured")
	}
	enc, err := s.cipher.Encrypt(value)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE project_secrets SET value_enc = ?, updated_at = ? WHERE project_id = ? AND name = ?`,
		enc, time.Now().UTC().Format(time.RFC3339), projectID, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProjectSecret removes one secret by (projectID, name). Returns
// ErrNotFound if no such secret exists.
func (s *Store) DeleteProjectSecret(ctx context.Context, projectID int64, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM project_secrets WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// deleteProjectSecrets removes every secret for a project — called from
// DeleteProject, mirroring deleteDriftIgnores/deleteProjectRevisions.
func (s *Store) deleteProjectSecrets(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_secrets WHERE project_id = ?`, projectID)
	return err
}

// ResolveProjectSecretEnv decrypts every secret for a project into a plain
// name->value map. The only function in this file that decrypts — callers
// must never return its result in an HTTP response, log it, or persist it.
func (s *Store) ResolveProjectSecretEnv(ctx context.Context, projectID int64) (map[string]string, error) {
	if s.cipher == nil {
		return nil, errors.New("store: cipher not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, value_enc FROM project_secrets WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, enc string
		if err := rows.Scan(&name, &enc); err != nil {
			return nil, err
		}
		value, err := s.cipher.Decrypt(enc)
		if err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

// MaskSecretValue returns value's placeholder for use anywhere the real
// value must never appear: compose interpolation for preview/validate, and
// redacting the live side of a diff. Same value -> same placeholder;
// different value -> a different one; the real value cannot be recovered
// from it.
func (s *Store) MaskSecretValue(value string) string {
	return "secret:" + s.cipher.Fingerprint(value)
}
