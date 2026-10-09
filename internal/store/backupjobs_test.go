package store

import (
	"testing"
	"time"
)

func TestBackupJobsCRUD(t *testing.T) {
	s, ctx := newStore(t)

	job := &BackupJob{
		Name: "nightly", Enabled: true, Scope: BackupScopeVolume, VolumeName: "data",
		HostID: 1, Image: "restic/restic", Command: "restic backup /data", IntervalMinutes: 60,
		CreatedBy: "admin",
	}
	id, err := s.CreateBackupJob(ctx, job, map[string]string{"RESTIC_PASSWORD": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.BackupJobByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "nightly" || got.Scope != BackupScopeVolume || got.VolumeName != "data" || got.IntervalMinutes != 60 {
		t.Errorf("BackupJobByID: %+v", got)
	}

	list, err := s.ListBackupJobs(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListBackupJobs: %+v err=%v", list, err)
	}

	if err := s.UpdateBackupJob(ctx, id, &BackupJob{
		Name: "nightly2", Scope: BackupScopeVolume, VolumeName: "data2", HostID: 1,
		Image: "restic/restic", Command: "restic backup /data", IntervalMinutes: 30,
	}, map[string]string{"RESTIC_PASSWORD": "newpw"}, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.BackupJobByID(ctx, id)
	if got.Name != "nightly2" || got.VolumeName != "data2" || got.IntervalMinutes != 30 {
		t.Errorf("UpdateBackupJob not applied: %+v", got)
	}

	if err := s.SetBackupJobEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.BackupJobByID(ctx, id)
	if got.Enabled {
		t.Error("expected job disabled")
	}

	if err := s.DeleteBackupJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackupJobByID(ctx, id); err != ErrNotFound {
		t.Errorf("deleted job should be ErrNotFound, got %v", err)
	}
}

// The list/get API type never returns env — it's write-only, matching how
// Registry's list type omits Password.
func TestBackupJobEnv_EncryptedAndWriteOnly(t *testing.T) {
	s, ctx := newStore(t)
	id, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "j", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, map[string]string{"SECRET": "hunter2"})
	if err != nil {
		t.Fatal(err)
	}

	// The raw row must not contain the plaintext secret.
	var enc string
	if err := s.db.QueryRowContext(ctx, `SELECT env_enc FROM backup_jobs WHERE id = ?`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || enc == `{"SECRET":"hunter2"}` {
		t.Errorf("env should be stored encrypted, got %q", enc)
	}

	env, err := s.BackupJobEnv(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if env["SECRET"] != "hunter2" {
		t.Errorf("BackupJobEnv decrypt: %+v", env)
	}

	// BackupJob (from List/Get) has no field that could leak it back out.
	job, _ := s.BackupJobByID(ctx, id)
	_ = job // BackupJob has no Env field at all — a compile-time guarantee, not a runtime check.
}

// An update with no env (the write-only form field left blank) must not wipe
// the previously stored secret — the same "blank keeps the existing value"
// convention SetSMTP/SetLDAP use.
func TestUpdateBackupJob_BlankEnvKeepsExisting(t *testing.T) {
	s, ctx := newStore(t)
	id, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "j", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, map[string]string{"RESTIC_PASSWORD": "original"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateBackupJob(ctx, id, &BackupJob{
		Name: "j2", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, nil, false); err != nil {
		t.Fatal(err)
	}

	env, err := s.BackupJobEnv(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if env["RESTIC_PASSWORD"] != "original" {
		t.Errorf("blank env on update should preserve the stored secret, got %+v", env)
	}

	// A non-empty env on a later update still replaces it.
	if err := s.UpdateBackupJob(ctx, id, &BackupJob{
		Name: "j2", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, map[string]string{"RESTIC_PASSWORD": "rotated"}, false); err != nil {
		t.Fatal(err)
	}
	env, _ = s.BackupJobEnv(ctx, id)
	if env["RESTIC_PASSWORD"] != "rotated" {
		t.Errorf("non-empty env on update should replace the stored secret, got %+v", env)
	}

	// clearEnv explicitly removes the stored secret, even with an empty map.
	if err := s.UpdateBackupJob(ctx, id, &BackupJob{
		Name: "j2", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, nil, true); err != nil {
		t.Fatal(err)
	}
	env, err = s.BackupJobEnv(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Errorf("clearEnv should remove the stored secret, got %+v", env)
	}
}

func TestDueBackupJobs(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now()

	// Manual-only (interval 0) is never due, even if never run.
	manualID, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "manual", Enabled: true, Scope: BackupScopeVolume, VolumeName: "v",
		Image: "busybox", Command: "true", IntervalMinutes: 0,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Never run, interval set: due immediately.
	neverRunID, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "never-run", Enabled: true, Scope: BackupScopeVolume, VolumeName: "v",
		Image: "busybox", Command: "true", IntervalMinutes: 60,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Ran recently: not due.
	recentID, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "recent", Enabled: true, Scope: BackupScopeVolume, VolumeName: "v",
		Image: "busybox", Command: "true", IntervalMinutes: 60,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordBackupRun(ctx, &BackupRun{JobID: recentID, StartedAt: now, FinishedAt: now, OK: true}); err != nil {
		t.Fatal(err)
	}

	// Ran long ago: due again.
	staleID, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "stale", Enabled: true, Scope: BackupScopeVolume, VolumeName: "v",
		Image: "busybox", Command: "true", IntervalMinutes: 60,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * time.Hour)
	if _, err := s.RecordBackupRun(ctx, &BackupRun{JobID: staleID, StartedAt: old, FinishedAt: old, OK: true}); err != nil {
		t.Fatal(err)
	}

	// Disabled, never run, interval set: never due.
	disabledID, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "disabled", Enabled: false, Scope: BackupScopeVolume, VolumeName: "v",
		Image: "busybox", Command: "true", IntervalMinutes: 60,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	due, err := s.DueBackupJobs(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	dueIDs := map[int64]bool{}
	for _, j := range due {
		dueIDs[j.ID] = true
	}
	if dueIDs[manualID] {
		t.Error("manual-only job should never be due")
	}
	if !dueIDs[neverRunID] {
		t.Error("never-run job with an interval should be due")
	}
	if dueIDs[recentID] {
		t.Error("recently-run job should not be due yet")
	}
	if !dueIDs[staleID] {
		t.Error("job last run beyond its interval should be due")
	}
	if dueIDs[disabledID] {
		t.Error("disabled job should never be due")
	}
}

func TestRecordBackupRun_UpdatesJobAndHistory(t *testing.T) {
	s, ctx := newStore(t)
	id, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "j", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := s.RecordBackupRun(ctx, &BackupRun{
		JobID: id, StartedAt: start, FinishedAt: start.Add(time.Second), OK: false,
		ExitCode: 1, Output: "boom", Error: "exit code 1", TriggeredBy: "schedule",
	}); err != nil {
		t.Fatal(err)
	}

	job, err := s.BackupJobByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.LastRunAt == nil || job.LastRunOK || job.LastRunDetail != "exit code 1" {
		t.Errorf("denormalized last_run_* not updated: %+v", job)
	}

	runs, err := s.ListBackupRuns(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Output != "boom" || runs[0].TriggeredBy != "schedule" {
		t.Errorf("ListBackupRuns: %+v", runs)
	}

	// A second, successful run clears the detail and flips ok.
	if _, err := s.RecordBackupRun(ctx, &BackupRun{
		JobID: id, StartedAt: start, FinishedAt: start.Add(2 * time.Second), OK: true, TriggeredBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	job, _ = s.BackupJobByID(ctx, id)
	if !job.LastRunOK || job.LastRunDetail != "" {
		t.Errorf("expected last run to flip to ok with no detail: %+v", job)
	}
	runs, _ = s.ListBackupRuns(ctx, id, 0)
	if len(runs) != 2 || runs[0].TriggeredBy != "alice" {
		t.Errorf("expected 2 runs, newest first: %+v", runs)
	}
}

// DeleteBackupJob must clean up run history too — otherwise it accumulates
// forever as orphans nothing will ever read again.
func TestDeleteBackupJob_RemovesRuns(t *testing.T) {
	s, ctx := newStore(t)
	id, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "j", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordBackupRun(ctx, &BackupRun{JobID: id, StartedAt: time.Now(), FinishedAt: time.Now(), OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBackupJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE job_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected run history to be removed with the job, found %d", n)
	}
}

// A single job's history is pruned to maxBackupRunsPerJob — the per-run
// output cap alone doesn't bound the table, only the row count does.
func TestRecordBackupRun_PrunesHistoryBeyondLimit(t *testing.T) {
	s, ctx := newStore(t)
	id, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "j", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().Truncate(time.Second)
	total := maxBackupRunsPerJob + 10
	for i := 0; i < total; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		if _, err := s.RecordBackupRun(ctx, &BackupRun{JobID: id, StartedAt: ts, FinishedAt: ts, OK: true}); err != nil {
			t.Fatal(err)
		}
	}

	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE job_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != maxBackupRunsPerJob {
		t.Errorf("expected history pruned to %d rows, got %d", maxBackupRunsPerJob, n)
	}

	// The most recent run must survive the prune, not an arbitrary one.
	runs, err := s.ListBackupRuns(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantLatest := base.Add(time.Duration(total-1) * time.Second)
	if len(runs) != 1 || !runs[0].StartedAt.Equal(wantLatest) {
		t.Errorf("expected the newest run to survive pruning, got %+v (want started_at %v)", runs, wantLatest)
	}
}

// RecordBackupRun must be atomic: if the job it belongs to no longer exists
// (deleted concurrently, e.g. by a scheduler racing a delete), the run insert
// rolls back too rather than leaving an orphaned history row for a job the
// scheduler will never revisit.
func TestRecordBackupRun_RollsBackWhenJobIsGone(t *testing.T) {
	s, ctx := newStore(t)
	id, err := s.CreateBackupJob(ctx, &BackupJob{
		Name: "j", Scope: BackupScopeVolume, VolumeName: "v", Image: "busybox", Command: "true",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBackupJob(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := s.RecordBackupRun(ctx, &BackupRun{JobID: id, StartedAt: time.Now(), FinishedAt: time.Now(), OK: true}); err != ErrNotFound {
		t.Fatalf("RecordBackupRun for a deleted job = %v, want ErrNotFound", err)
	}

	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE job_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected the run insert to be rolled back, found %d orphaned rows", n)
	}
}
