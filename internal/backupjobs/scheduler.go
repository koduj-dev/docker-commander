// Package backupjobs schedules and runs Volume backup jobs — a trigger-and-
// status wrapper around a user-supplied backup command (restic/borg/etc,
// already pointed at its own repository) run against a volume's or project's
// data via a short-lived Docker helper container (see
// internal/docker/volumebackup.go). Not a backup engine: no repository,
// retention or storage logic of our own — only trigger + status.
package backupjobs

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

const (
	tickInterval = 1 * time.Minute
	runTimeout   = 30 * time.Minute

	// TriggeredBySchedule marks a run started by the scheduler rather than a
	// user-initiated "Run now".
	TriggeredBySchedule = "schedule"
)

// Run polls for due jobs once a minute until ctx is cancelled, running each
// one that's due. A plain ticker, mirroring monitor.Monitor's
// watchManagerLoop shape, not a general cron — this app has no cron parser
// anywhere else and shouldn't gain one just for this.
func Run(ctx context.Context, st *store.Store, dm *docker.Manager) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick(ctx, st, dm)
		}
	}
}

func tick(ctx context.Context, st *store.Store, dm *docker.Manager) {
	due, err := st.DueBackupJobs(ctx, time.Now())
	if err != nil {
		log.Printf("backupjobs: list due jobs: %v", err)
		return
	}
	for _, job := range due {
		if err := RunJob(ctx, st, dm, job, TriggeredBySchedule); err != nil {
			log.Printf("backupjobs: job %q: %v", job.Name, err)
		}
	}
}

// TriggerNow runs one job immediately, bypassing its schedule — used by the
// manual "Run now" API action. triggeredBy is the acting username.
func TriggerNow(ctx context.Context, st *store.Store, dm *docker.Manager, jobID int64, triggeredBy string) error {
	job, err := st.BackupJobByID(ctx, jobID)
	if err != nil {
		return err
	}
	return RunJob(ctx, st, dm, *job, triggeredBy)
}

// RunJob executes one backup job's command via a helper container and records
// the outcome as a new run — including a failure to even resolve the job's
// mounts/env, so "why didn't this ever run" is answered in the same history,
// not silently swallowed.
func RunJob(ctx context.Context, st *store.Store, dm *docker.Manager, job store.BackupJob, triggeredBy string) error {
	started := time.Now()

	hostID, mounts, err := resolveTarget(ctx, st, dm, job)
	if err != nil {
		recordFailure(ctx, st, job.ID, started, triggeredBy, err)
		return err
	}
	env, err := st.BackupJobEnv(ctx, job.ID)
	if err != nil {
		recordFailure(ctx, st, job.ID, started, triggeredBy, err)
		return err
	}

	output, exitCode, runErr := dm.RunBackupJob(ctx, hostID, job.Image, job.Command, env, mounts, runTimeout)
	run := &store.BackupRun{
		JobID: job.ID, StartedAt: started, FinishedAt: time.Now(),
		OK: runErr == nil && exitCode == 0, ExitCode: exitCode, Output: output,
		TriggeredBy: triggeredBy,
	}
	switch {
	case runErr != nil:
		run.Error = runErr.Error()
	case exitCode != 0:
		run.Error = fmt.Sprintf("exit code %d", exitCode)
	}
	if _, err := st.RecordBackupRun(ctx, run); err != nil {
		log.Printf("backupjobs: record run for job %d: %v", job.ID, err)
	}
	return runErr
}

// resolveTarget resolves which host to run on and the volume-name -> mount-path
// map for a job's scope. For a project-scoped job the host is always resolved
// fresh from the project (not a possibly-stale copy on the job row) so a
// project that later moves host doesn't silently keep backing up the old one.
func resolveTarget(ctx context.Context, st *store.Store, dm *docker.Manager, job store.BackupJob) (hostID int64, mounts map[string]string, err error) {
	switch job.Scope {
	case store.BackupScopeVolume:
		if job.VolumeName == "" {
			return 0, nil, fmt.Errorf("volume-scoped job has no volume configured")
		}
		return job.HostID, map[string]string{job.VolumeName: "/data"}, nil
	case store.BackupScopeProject:
		proj, err := st.ProjectByID(ctx, job.ProjectID)
		if err != nil {
			return 0, nil, fmt.Errorf("resolve project: %w", err)
		}
		names, err := dm.ProjectVolumeNames(ctx, proj.HostID, proj.Slug)
		if err != nil {
			return 0, nil, fmt.Errorf("resolve project volumes: %w", err)
		}
		if len(names) == 0 {
			return 0, nil, fmt.Errorf("project %q has no named volumes to back up", proj.Slug)
		}
		mounts := make(map[string]string, len(names))
		for _, n := range names {
			mounts[n] = "/data/" + n
		}
		return proj.HostID, mounts, nil
	default:
		return 0, nil, fmt.Errorf("unknown backup job scope %q", job.Scope)
	}
}

func recordFailure(ctx context.Context, st *store.Store, jobID int64, started time.Time, triggeredBy string, err error) {
	run := &store.BackupRun{
		JobID: jobID, StartedAt: started, FinishedAt: time.Now(),
		OK: false, Error: err.Error(), TriggeredBy: triggeredBy,
	}
	if _, rerr := st.RecordBackupRun(ctx, run); rerr != nil {
		log.Printf("backupjobs: record failure for job %d: %v", jobID, rerr)
	}
}
