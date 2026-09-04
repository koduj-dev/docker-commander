package store

import (
	"context"
	"time"
)

// ProjectDriftIgnore is one (service, kind) drift a human has reviewed and
// deliberately accepted for a project, so future deploy previews stop
// counting it as active drift — see internal/docker/preview.go's
// ServiceChange.Ignored.
//
// Fingerprint pins the acceptance to the SPECIFIC from/to/detail values that
// were reviewed, not just the (service, kind) pair: without it, accepting one
// env-var change on "web" would silently also accept every future, unrelated
// env-var change on "web" — a different key, a different value, forever.
// MarkIgnoredChanges only marks a change ignored when both the pair AND the
// fingerprint match.
type ProjectDriftIgnore struct {
	Service     string    `json:"service"`
	Kind        string    `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ListDriftIgnores returns every ignored (service, kind) drift for a project.
func (s *Store) ListDriftIgnores(ctx context.Context, projectID int64) ([]ProjectDriftIgnore, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT service, kind, fingerprint, created_at FROM project_drift_ignores WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectDriftIgnore{}
	for rows.Next() {
		var d ProjectDriftIgnore
		var created string
		if err := rows.Scan(&d.Service, &d.Kind, &d.Fingerprint, &created); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// IgnoreDrift records that the specific drift identified by (service, kind,
// fingerprint) on a project has been reviewed and accepted. There is at most
// one active ignore per (project, service, kind): re-ignoring the same
// fingerprint leaves its original CreatedAt untouched (idempotent), but
// ignoring a NEW fingerprint for that same (service, kind) replaces the old
// one — the old, different drift this pair used to describe is no longer
// what's being accepted, so it shouldn't stay silently suppressed either.
func (s *Store) IgnoreDrift(ctx context.Context, projectID int64, service, kind, fingerprint string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_drift_ignores (project_id, service, kind, fingerprint, created_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, service, kind) DO UPDATE SET
		   fingerprint = excluded.fingerprint,
		   created_at = CASE WHEN project_drift_ignores.fingerprint = excluded.fingerprint
		                     THEN project_drift_ignores.created_at ELSE excluded.created_at END`,
		projectID, service, kind, fingerprint, now)
	return err
}

// UnignoreDrift removes one ignored drift, so the next preview counts it
// again, regardless of which fingerprint it was last ignored for.
func (s *Store) UnignoreDrift(ctx context.Context, projectID int64, service, kind string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_drift_ignores WHERE project_id = ? AND service = ? AND kind = ?`,
		projectID, service, kind)
	return err
}

// ClearDriftIgnores removes every ignored drift for a project. Called after a
// successful deploy (see Server.captureRevision): a redeploy is the moment
// the reviewed state is superseded, so an ignore should not outlive the
// deploy it was scoped to — otherwise a coincidentally identical drift
// reappearing later would be silently re-accepted without a human looking at
// it again.
func (s *Store) ClearDriftIgnores(ctx context.Context, projectID int64) error {
	return s.deleteDriftIgnores(ctx, projectID)
}

// deleteDriftIgnores removes every ignored drift for a project — called from
// DeleteProject so these rows don't outlive the project they describe, and
// from ClearDriftIgnores above after a deploy.
func (s *Store) deleteDriftIgnores(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_drift_ignores WHERE project_id = ?`, projectID)
	return err
}
