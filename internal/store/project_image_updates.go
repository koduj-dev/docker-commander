package store

import (
	"context"
	"time"
)

// LastNotifiedImageDigests returns the digest last notified about for each of
// a project's services, keyed by service name. A service absent from the map
// has never had a notification sent for it.
func (s *Store) LastNotifiedImageDigests(ctx context.Context, projectID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service, last_notified_digest FROM project_image_update_state WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var service, digest string
		if err := rows.Scan(&service, &digest); err != nil {
			return nil, err
		}
		out[service] = digest
	}
	return out, rows.Err()
}

// SetLastNotifiedImageDigest records the digest just notified about for one
// project's service, so a later poll finding the same drift doesn't notify
// again.
func (s *Store) SetLastNotifiedImageDigest(ctx context.Context, projectID int64, service, digest string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_image_update_state (project_id, service, last_notified_digest, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, service) DO UPDATE SET
			last_notified_digest = excluded.last_notified_digest,
			updated_at = excluded.updated_at`,
		projectID, service, digest, now)
	return err
}

// deleteProjectImageUpdateState removes a project's dedup state, called from
// DeleteProject — it describes this specific project's polling history and
// outlives nothing useful once the project is gone.
func (s *Store) deleteProjectImageUpdateState(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_image_update_state WHERE project_id = ?`, projectID)
	return err
}
