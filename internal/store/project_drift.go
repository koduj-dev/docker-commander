package store

import (
	"context"
	"time"
)

// ProjectDriftIgnore is one (service, kind) drift a human has reviewed and
// deliberately accepted for a project, so future deploy previews stop
// counting it as active drift — see internal/docker/preview.go's
// ServiceChange.Ignored.
type ProjectDriftIgnore struct {
	Service   string    `json:"service"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListDriftIgnores returns every ignored (service, kind) drift for a project.
func (s *Store) ListDriftIgnores(ctx context.Context, projectID int64) ([]ProjectDriftIgnore, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT service, kind, created_at FROM project_drift_ignores WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectDriftIgnore{}
	for rows.Next() {
		var d ProjectDriftIgnore
		var created string
		if err := rows.Scan(&d.Service, &d.Kind, &created); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// IgnoreDrift records that a service+kind drift on a project has been
// reviewed and accepted. Idempotent: re-ignoring one already ignored leaves
// its original CreatedAt untouched.
func (s *Store) IgnoreDrift(ctx context.Context, projectID int64, service, kind string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_drift_ignores (project_id, service, kind, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(project_id, service, kind) DO NOTHING`,
		projectID, service, kind, time.Now().UTC().Format(time.RFC3339))
	return err
}

// UnignoreDrift removes one ignored drift, so the next preview counts it
// again.
func (s *Store) UnignoreDrift(ctx context.Context, projectID int64, service, kind string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_drift_ignores WHERE project_id = ? AND service = ? AND kind = ?`,
		projectID, service, kind)
	return err
}

// deleteDriftIgnores removes every ignored drift for a project — called from
// DeleteProject so these rows don't outlive the project they describe.
func (s *Store) deleteDriftIgnores(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_drift_ignores WHERE project_id = ?`, projectID)
	return err
}
