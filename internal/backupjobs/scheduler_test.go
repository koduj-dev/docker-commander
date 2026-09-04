package backupjobs

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	dockerclient "github.com/docker/docker/client"

	"github.com/docker/docker/api/types/volume"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func createTestVolume(ctx context.Context, cli *dockerclient.Client) (string, error) {
	v, err := cli.VolumeCreate(ctx, volume.CreateOptions{})
	if err != nil {
		return "", err
	}
	return v.Name, nil
}

func createLabeledVolume(ctx context.Context, cli *dockerclient.Client, project string) (string, error) {
	v, err := cli.VolumeCreate(ctx, volume.CreateOptions{
		Labels: map[string]string{"com.docker.compose.project": project},
	})
	if err != nil {
		return "", err
	}
	return v.Name, nil
}

func removeTestVolume(cli *dockerclient.Client, name string) error {
	return cli.VolumeRemove(context.Background(), name, true)
}

func newTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(c)
	return st, context.Background()
}

func newTestManager(t *testing.T, st *store.Store) (*docker.Manager, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	dm := docker.NewManager(st)
	t.Cleanup(dm.Close)
	if _, err := dm.SystemInfo(ctx, 0); err != nil {
		t.Skipf("docker daemon not available: %v", err)
	}
	return dm, ctx
}

// A due job runs and its outcome is recorded.
func TestRunJob_VolumeScope_RecordsSuccess(t *testing.T) {
	st, _ := newTestStore(t)
	dm, ctx := newTestManager(t, st)

	cli, err := dm.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	vol, err := createTestVolume(ctx, cli)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeTestVolume(cli, vol) })

	id, err := st.CreateBackupJob(ctx, &store.BackupJob{
		Name: "vol-job", Enabled: true, Scope: store.BackupScopeVolume, VolumeName: vol,
		Image: "alpine:latest", Command: "echo hi",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.BackupJobByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if err := RunJob(ctx, st, dm, *job, TriggeredBySchedule); err != nil {
		t.Fatalf("RunJob: %v", err)
	}

	got, err := st.BackupJobByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastRunOK || got.LastRunAt == nil {
		t.Errorf("expected last run recorded as ok: %+v", got)
	}
	runs, _ := st.ListBackupRuns(ctx, id, 0)
	if len(runs) != 1 || runs[0].TriggeredBy != TriggeredBySchedule {
		t.Errorf("ListBackupRuns: %+v", runs)
	}
}

// A project-scoped job backs up every named volume the project actually
// created (found via the compose label), not a guess from a declared name.
func TestRunJob_ProjectScope_BacksUpAllProjectVolumes(t *testing.T) {
	st, _ := newTestStore(t)
	dm, ctx := newTestManager(t, st)

	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "dc-backupjobs-test-app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}

	cli, err := dm.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := createLabeledVolume(ctx, cli, "dc-backupjobs-test-app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeTestVolume(cli, v1) })
	v2, err := createLabeledVolume(ctx, cli, "dc-backupjobs-test-app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeTestVolume(cli, v2) })
	// A volume belonging to a DIFFERENT project must never be touched.
	other, err := createLabeledVolume(ctx, cli, "dc-backupjobs-test-other")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeTestVolume(cli, other) })

	id, err := st.CreateBackupJob(ctx, &store.BackupJob{
		Name: "proj-job", Enabled: true, Scope: store.BackupScopeProject, ProjectID: pid,
		Image: "alpine:latest", Command: "echo hi",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.BackupJobByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if err := RunJob(ctx, st, dm, *job, TriggeredBySchedule); err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	got, _ := st.BackupJobByID(ctx, id)
	if !got.LastRunOK {
		t.Errorf("expected project-scoped run to succeed: %+v", got)
	}
}

// TriggerNow bypasses the interval check entirely.
func TestTriggerNow_BypassesInterval(t *testing.T) {
	st, _ := newTestStore(t)
	dm, ctx := newTestManager(t, st)

	cli, err := dm.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	vol, err := createTestVolume(ctx, cli)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeTestVolume(cli, vol) })

	id, err := st.CreateBackupJob(ctx, &store.BackupJob{
		Name: "manual", Enabled: true, Scope: store.BackupScopeVolume, VolumeName: vol,
		Image: "alpine:latest", Command: "echo hi", IntervalMinutes: 60,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Just ran: DueBackupJobs would say "not due" for another 59 minutes.
	if _, err := st.RecordBackupRun(ctx, &store.BackupRun{
		JobID: id, StartedAt: time.Now(), FinishedAt: time.Now(), OK: true, TriggeredBy: TriggeredBySchedule,
	}); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueBackupJobs(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range due {
		if j.ID == id {
			t.Fatal("test setup invalid: job should not be due yet")
		}
	}

	if err := TriggerNow(ctx, st, dm, id, "alice"); err != nil {
		t.Fatalf("TriggerNow: %v", err)
	}
	runs, _ := st.ListBackupRuns(ctx, id, 0)
	if len(runs) != 2 || runs[0].TriggeredBy != "alice" {
		t.Errorf("expected TriggerNow to add a manual run regardless of schedule: %+v", runs)
	}
}

// A volume-scoped job with no configured volume fails clean with a recorded
// run, rather than panicking or silently mounting nothing.
func TestRunJob_VolumeScope_MissingVolumeIsRecordedAsFailure(t *testing.T) {
	st, ctx := newTestStore(t)
	id, err := st.CreateBackupJob(ctx, &store.BackupJob{
		Name: "broken", Scope: store.BackupScopeVolume, Image: "alpine:latest", Command: "echo hi",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.BackupJobByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// resolveTarget must fail before ever touching dm, so a nil Manager here
	// still proves the failure is caught, not merely "didn't crash".
	if err := RunJob(ctx, st, nil, *job, TriggeredBySchedule); err == nil {
		t.Fatal("expected an error for a volume-scoped job with no volume configured")
	}
	runs, _ := st.ListBackupRuns(ctx, id, 0)
	if len(runs) != 1 || runs[0].OK {
		t.Errorf("expected the resolution failure to be recorded as a failed run: %+v", runs)
	}
}
