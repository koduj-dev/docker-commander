package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Backup job scopes.
const (
	BackupScopeVolume  = "volume"
	BackupScopeProject = "project"
)

// BackupJob is a trigger-and-status wrapper around a user-supplied backup
// command run against a volume's or project's data. The env map is
// write-only (encrypted at rest) and never comes back through this type —
// see BackupJobEnv, used only by the runner.
type BackupJob struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	Scope           string     `json:"scope"` // volume | project
	VolumeName      string     `json:"volumeName"`
	ProjectID       int64      `json:"projectId"`
	HostID          int64      `json:"hostId"`
	Image           string     `json:"image"`
	Command         string     `json:"command"`
	IntervalMinutes int        `json:"intervalMinutes"` // 0 = manual only
	CreatedBy       string     `json:"createdBy"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	LastRunAt       *time.Time `json:"lastRunAt"`
	LastRunOK       bool       `json:"lastRunOk"`
	LastRunDetail   string     `json:"lastRunDetail"`
}

// BackupRun is one recorded execution of a backup job.
type BackupRun struct {
	ID          int64     `json:"id"`
	JobID       int64     `json:"jobId"`
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt"`
	OK          bool      `json:"ok"`
	ExitCode    int       `json:"exitCode"`
	Output      string    `json:"output"`
	Error       string    `json:"error"`
	TriggeredBy string    `json:"triggeredBy"` // "schedule" or a username
}

// maxBackupRunOutput caps how much captured output a single run stores, so a
// runaway command can't grow the database unbounded.
const maxBackupRunOutput = 256 << 10

// ListBackupJobs returns every configured backup job, without env.
func (s *Store) ListBackupJobs(ctx context.Context) ([]BackupJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, enabled, scope, volume_name, project_id, host_id, image, command,
		       interval_minutes, created_by, created_at, updated_at, last_run_at, last_run_ok, last_run_detail
		FROM backup_jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupJob
	for rows.Next() {
		j, err := scanBackupJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// BackupJobByID returns one backup job by ID (ErrNotFound if missing).
func (s *Store) BackupJobByID(ctx context.Context, id int64) (*BackupJob, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, enabled, scope, volume_name, project_id, host_id, image, command,
		       interval_minutes, created_by, created_at, updated_at, last_run_at, last_run_ok, last_run_detail
		FROM backup_jobs WHERE id = ?`, id)
	return scanBackupJob(row)
}

// CreateBackupJob inserts a backup job, encrypting env, and returns its ID.
func (s *Store) CreateBackupJob(ctx context.Context, j *BackupJob, env map[string]string) (int64, error) {
	enc, err := s.encryptEnv(env)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_jobs (name, enabled, scope, volume_name, project_id, host_id, image, command, env_enc,
		                         interval_minutes, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Name, boolToInt(j.Enabled), j.Scope, j.VolumeName, j.ProjectID, j.HostID, j.Image, j.Command, enc,
		j.IntervalMinutes, j.CreatedBy, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateBackupJob replaces a backup job's mutable fields, including env
// (enabled is managed separately via SetBackupJobEnabled). An empty env
// leaves the stored one untouched — the same "blank keeps the existing
// secret" convention SetSMTP/SetLDAP use — since env is write-only and a form
// re-displaying it would otherwise have to blank it out on every edit,
// silently wiping credentials whenever an unrelated field changes.
func (s *Store) UpdateBackupJob(ctx context.Context, id int64, j *BackupJob, env map[string]string) error {
	var enc string
	if len(env) == 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT env_enc FROM backup_jobs WHERE id = ?`, id).Scan(&enc); err != nil {
			return err
		}
	} else {
		var err error
		enc, err = s.encryptEnv(env)
		if err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_jobs
		SET name = ?, scope = ?, volume_name = ?, project_id = ?, host_id = ?, image = ?, command = ?,
		    env_enc = ?, interval_minutes = ?, updated_at = ?
		WHERE id = ?`,
		j.Name, j.Scope, j.VolumeName, j.ProjectID, j.HostID, j.Image, j.Command, enc,
		j.IntervalMinutes, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// SetBackupJobEnabled toggles a backup job on or off.
func (s *Store) SetBackupJobEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_jobs SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	return err
}

// DeleteBackupJob removes a backup job and its run history.
func (s *Store) DeleteBackupJob(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM backup_runs WHERE job_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_jobs WHERE id = ?`, id)
	return err
}

// BackupJobEnv returns a job's decrypted environment. Only ever called by the
// runner — never surfaced through the API's list/get responses.
func (s *Store) BackupJobEnv(ctx context.Context, id int64) (map[string]string, error) {
	if s.cipher == nil {
		return nil, errors.New("store: cipher not configured")
	}
	var enc string
	err := s.db.QueryRowContext(ctx, `SELECT env_enc FROM backup_jobs WHERE id = ?`, id).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if enc == "" {
		return map[string]string{}, nil
	}
	plain, err := s.cipher.Decrypt(enc)
	if err != nil {
		return nil, err
	}
	return decodeJSON(plain), nil
}

// DueBackupJobs returns enabled, interval-scheduled jobs whose interval has
// elapsed since their last run (or that have never run).
func (s *Store) DueBackupJobs(ctx context.Context, now time.Time) ([]BackupJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, enabled, scope, volume_name, project_id, host_id, image, command,
		       interval_minutes, created_by, created_at, updated_at, last_run_at, last_run_ok, last_run_detail
		FROM backup_jobs WHERE enabled = 1 AND interval_minutes > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupJob
	for rows.Next() {
		j, err := scanBackupJob(rows)
		if err != nil {
			return nil, err
		}
		due := j.LastRunAt == nil || now.Sub(*j.LastRunAt) >= time.Duration(j.IntervalMinutes)*time.Minute
		if due {
			out = append(out, *j)
		}
	}
	return out, rows.Err()
}

// RecordBackupRun appends a run to a job's history and updates the job's
// denormalized last_run_* fields for O(1) status badge lookups.
func (s *Store) RecordBackupRun(ctx context.Context, run *BackupRun) (int64, error) {
	output := run.Output
	if len(output) > maxBackupRunOutput {
		output = output[:maxBackupRunOutput]
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_runs (job_id, started_at, finished_at, ok, exit_code, output, error, triggered_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.JobID, run.StartedAt.UTC().Format(time.RFC3339), run.FinishedAt.UTC().Format(time.RFC3339),
		boolToInt(run.OK), run.ExitCode, output, run.Error, run.TriggeredBy)
	if err != nil {
		return 0, err
	}
	detail := run.Error
	if run.OK {
		detail = ""
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE backup_jobs SET last_run_at = ?, last_run_ok = ?, last_run_detail = ? WHERE id = ?`,
		run.FinishedAt.UTC().Format(time.RFC3339), boolToInt(run.OK), detail, run.JobID); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListBackupRuns returns a job's run history, most recent first.
func (s *Store) ListBackupRuns(ctx context.Context, jobID int64, limit int) ([]BackupRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_id, started_at, finished_at, ok, exit_code, output, error, triggered_by
		FROM backup_runs WHERE job_id = ? ORDER BY id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupRun
	for rows.Next() {
		var r BackupRun
		var started, finished string
		var ok int
		if err := rows.Scan(&r.ID, &r.JobID, &started, &finished, &ok, &r.ExitCode, &r.Output, &r.Error, &r.TriggeredBy); err != nil {
			return nil, err
		}
		r.OK = ok != 0
		r.StartedAt, _ = time.Parse(time.RFC3339, started)
		r.FinishedAt, _ = time.Parse(time.RFC3339, finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

// encryptEnv marshals then encrypts a job's environment for storage.
func (s *Store) encryptEnv(env map[string]string) (string, error) {
	if s.cipher == nil {
		return "", errors.New("store: cipher not configured")
	}
	if len(env) == 0 {
		return "", nil
	}
	return s.cipher.Encrypt(encodeJSON(env))
}

// scanBackupJob scans a backup_jobs row (query or single-row).
func scanBackupJob(r scanner) (*BackupJob, error) {
	var j BackupJob
	var created, updated, lastRunAt string
	var enabled, lastRunOK int
	err := r.Scan(&j.ID, &j.Name, &enabled, &j.Scope, &j.VolumeName, &j.ProjectID, &j.HostID, &j.Image, &j.Command,
		&j.IntervalMinutes, &j.CreatedBy, &created, &updated, &lastRunAt, &lastRunOK, &j.LastRunDetail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Enabled = enabled != 0
	j.LastRunOK = lastRunOK != 0
	j.CreatedAt, _ = time.Parse(time.RFC3339, created)
	j.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	if lastRunAt != "" {
		if t, err := time.Parse(time.RFC3339, lastRunAt); err == nil {
			j.LastRunAt = &t
		}
	}
	return &j, nil
}
