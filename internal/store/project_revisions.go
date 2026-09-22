package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RevisionImage is what one service's image resolved to at the moment a
// revision was captured — both the reference as written (which may be a
// mutable tag) and, best-effort, the digest actually running, so a later
// restore can pin to the exact image that ran then rather than whatever a
// mutable tag resolves to today.
type RevisionImage struct {
	Service string `json:"service"`
	Image   string `json:"image"`
	Digest  string `json:"digest,omitempty"`
}

// ProjectRevision is metadata for one successful deploy of a project. The
// actual files are a zip snapshot on disk — see the schema comment in
// store.go and Server.revisionZipPath.
type ProjectRevision struct {
	ID              int64           `json:"id"`
	ProjectID       int64           `json:"projectId"`
	Revision        int             `json:"revision"`
	HostID          int64           `json:"hostId"`
	Profiles        []string        `json:"profiles"`
	Images          []RevisionImage `json:"images"`
	Valid           bool            `json:"valid"`
	ValidationError string          `json:"validationError,omitempty"`
	Output          string          `json:"output,omitempty"`
	Author          string          `json:"author"`
	Reason          string          `json:"reason,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// CreateRevision numbers rev one past the project's current highest revision
// (1 for the first ever) and inserts it. Callers set every field except
// Revision/ID/CreatedAt, which this fills in.
//
// The number + insert run inside one transaction — not two unlocked
// statements — so two concurrent successful deploys of the SAME project
// can't both read the same max and collide on project_revisions' UNIQUE
// (project_id, revision) constraint. The store's single-connection pool
// (see store.go's SetMaxOpenConns(1)) means a second CreateRevision call
// simply blocks for a free connection until the first transaction commits,
// rather than racing it. maxRetries is belt-and-braces on top of that: it
// also protects a caller from ever silently losing a revision row if a
// collision somehow still occurs (e.g. a future change relaxes the
// single-connection assumption).
const createRevisionMaxRetries = 5

func (s *Store) CreateRevision(ctx context.Context, rev *ProjectRevision) (int64, error) {
	profilesJSON, err := json.Marshal(rev.Profiles)
	if err != nil {
		return 0, err
	}
	imagesJSON, err := json.Marshal(rev.Images)
	if err != nil {
		return 0, err
	}

	var id int64
	var createdAt string
	for attempt := 0; ; attempt++ {
		id, createdAt, err = s.createRevisionOnce(ctx, rev, string(profilesJSON), string(imagesJSON))
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "UNIQUE") || attempt >= createRevisionMaxRetries {
			return 0, err
		}
		// Another transaction inserted the same revision number between our
		// SELECT MAX and INSERT (only possible if something outside this
		// store's own single-connection serialization raced it) — retry with
		// a freshly read max rather than dropping this revision on the floor.
	}
	rev.ID = id
	rev.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return id, nil
}

// createRevisionOnce reads the current max and inserts the next revision
// number in one transaction, so no other CreateRevision call can observe or
// act on a max value this transaction is about to make stale.
func (s *Store) createRevisionOnce(ctx context.Context, rev *ProjectRevision, profilesJSON, imagesJSON string) (id int64, createdAt string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	var maxRev int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM project_revisions WHERE project_id = ?`, rev.ProjectID,
	).Scan(&maxRev); err != nil {
		return 0, "", err
	}
	nextRev := maxRev + 1

	createdAt = time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO project_revisions
			(project_id, revision, host_id, profiles, images, valid, validation_error, output, author, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rev.ProjectID, nextRev, rev.HostID, profilesJSON, imagesJSON,
		boolToInt(rev.Valid), rev.ValidationError, rev.Output, rev.Author, rev.Reason, createdAt)
	if err != nil {
		return 0, "", err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, "", err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	rev.Revision = nextRev
	return id, createdAt, nil
}

// ListRevisions returns every revision for a project, newest first.
func (s *Store) ListRevisions(ctx context.Context, projectID int64) ([]ProjectRevision, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, revision, host_id, profiles, images, valid, validation_error, output, author, reason, created_at
		FROM project_revisions WHERE project_id = ? ORDER BY revision DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectRevision{}
	for rows.Next() {
		rev, err := scanRevisionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rev)
	}
	return out, rows.Err()
}

// RevisionByNumber looks up one project's revision by its per-project number
// (the number an operator would say out loud), not the global row id.
func (s *Store) RevisionByNumber(ctx context.Context, projectID int64, revision int) (*ProjectRevision, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, revision, host_id, profiles, images, valid, validation_error, output, author, reason, created_at
		FROM project_revisions WHERE project_id = ? AND revision = ?`, projectID, revision)
	rev, err := scanRevisionRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return rev, err
}

// LatestRevision returns a project's most recent revision, or ErrNotFound if
// it has never been deployed.
func (s *Store) LatestRevision(ctx context.Context, projectID int64) (*ProjectRevision, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, revision, host_id, profiles, images, valid, validation_error, output, author, reason, created_at
		FROM project_revisions WHERE project_id = ? ORDER BY revision DESC LIMIT 1`, projectID)
	rev, err := scanRevisionRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return rev, err
}

func scanRevisionRow(row scanner) (*ProjectRevision, error) {
	var rev ProjectRevision
	var profilesJSON, imagesJSON, created string
	var valid int
	if err := row.Scan(&rev.ID, &rev.ProjectID, &rev.Revision, &rev.HostID, &profilesJSON, &imagesJSON,
		&valid, &rev.ValidationError, &rev.Output, &rev.Author, &rev.Reason, &created); err != nil {
		return nil, err
	}
	rev.Valid = valid != 0
	rev.CreatedAt, _ = time.Parse(time.RFC3339, created)
	_ = json.Unmarshal([]byte(profilesJSON), &rev.Profiles)
	_ = json.Unmarshal([]byte(imagesJSON), &rev.Images)
	if rev.Profiles == nil {
		rev.Profiles = []string{}
	}
	if rev.Images == nil {
		rev.Images = []RevisionImage{}
	}
	return &rev, nil
}

// SetRevisionValid atomically publishes (valid=true, validationError="") or
// fails (valid=false, validationError=<reason>) a revision's on-disk
// snapshot capture. CreateRevision always inserts a row with Valid=false
// first — this is the only thing that ever flips it to true, once the
// snapshot file has actually been written and renamed into place. A
// revision that never reaches SetRevisionValid(..., true, "") stays
// visible in history (so the failure itself is auditable) but is refused by
// restore, exactly like one explicitly marked invalid.
func (s *Store) SetRevisionValid(ctx context.Context, id int64, valid bool, validationError string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE project_revisions SET valid = ?, validation_error = ? WHERE id = ?`,
		boolToInt(valid), validationError, id)
	return err
}

// deleteProjectRevisions removes every revision row for a project — called
// from DeleteProject. The caller is responsible for removing the on-disk zip
// snapshots (Server.projectRevisionsDir), which the store layer knows nothing
// about.
func (s *Store) deleteProjectRevisions(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_revisions WHERE project_id = ?`, projectID)
	return err
}
