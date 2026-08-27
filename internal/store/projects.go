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
	HostID      int64 // target Docker host for deploy; 0 = local daemon
	// AllowRemoteHostPaths lets a remote deploy mount bind sources from OUTSIDE
	// the project folder — paths on the remote host itself, which are otherwise
	// refused because we can't see what they hold. Off by default; enabling it
	// requires the "hosts" permission and is audited.
	AllowRemoteHostPaths bool
	// LastDeployedProfiles is the profile list passed to the last successful
	// `compose up` for this project — what's actually running, as opposed to
	// whatever profiles a user has since selected for the NEXT deploy (that
	// selection is a client-only preference; see web/src/pages/Projects.tsx).
	// nil/empty means never successfully deployed, or deployed with no profiles.
	LastDeployedProfiles []string
	CreatedBy            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// CreateProject inserts a project and returns its ID. A slug collision yields
// ErrDuplicate.
func (s *Store) CreateProject(ctx context.Context, p *Project) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (name, slug, compose_file, host_id, allow_remote_host_paths, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Slug, orDefault(p.ComposeFile, "compose.yml"), p.HostID,
		boolToInt(p.AllowRemoteHostPaths), p.CreatedBy, now, now)
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
		SELECT id, name, slug, compose_file, host_id, allow_remote_host_paths, last_deployed_profiles, created_by, created_at, updated_at
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
		SELECT id, name, slug, compose_file, host_id, allow_remote_host_paths, last_deployed_profiles, created_by, created_at, updated_at
		FROM projects WHERE id = ?`, id))
}

// UpdateProjectSettings changes the display name, target host and the
// remote-host-path opt-in (the slug stays immutable).
func (s *Store) UpdateProjectSettings(ctx context.Context, id int64, name string, hostID int64, allowRemoteHostPaths bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET name = ?, host_id = ?, allow_remote_host_paths = ?, updated_at = ? WHERE id = ?`,
		name, hostID, boolToInt(allowRemoteHostPaths), time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// TouchProject bumps updated_at (called when a file changes).
func (s *Store) TouchProject(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// SetLastDeployedProfiles records the profiles used on a project's last
// successful `compose up`, so the UI can tell "currently deployed" apart from
// whatever's merely selected for the next deploy. Called only after a deploy
// actually succeeds — a failed deploy must not overwrite what's still running.
func (s *Store) SetLastDeployedProfiles(ctx context.Context, id int64, profiles []string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET last_deployed_profiles = ?, updated_at = ? WHERE id = ?`,
		marshalSections(profiles), time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// DeleteProject removes the project row (the caller removes the folder and
// any on-disk revision snapshots) and any drift ignores / revision metadata
// recorded against it — those describe a specific project's state, and
// outlive nothing useful once it's gone.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	if err := s.deleteDriftIgnores(ctx, id); err != nil {
		return err
	}
	if err := s.deleteProjectRevisions(ctx, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	return err
}

func scanProjectRow(row scanner) (*Project, error) {
	var p Project
	var createdAt, updatedAt, lastDeployedProfiles string
	var allowRemote int
	err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.ComposeFile, &p.HostID, &allowRemote, &lastDeployedProfiles, &p.CreatedBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.AllowRemoteHostPaths = allowRemote != 0
	p.LastDeployedProfiles = unmarshalSections(lastDeployedProfiles)
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &p, nil
}
