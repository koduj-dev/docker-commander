package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
)

// A job that writes a marker file into /data and exits 0 is captured as ok.
func TestRunBackupJob_SuccessCapturesOutput(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	vol, err := cli.VolumeCreate(ctx, volume.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), vol.Name, true) })

	output, exitCode, err := m.RunBackupJob(ctx, 0, testImage, "echo backed-up > /data/marker.txt && cat /data/marker.txt",
		nil, map[string]string{vol.Name: "/data"}, time.Minute)
	if err != nil {
		t.Fatalf("RunBackupJob: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if !strings.Contains(output, "backed-up") {
		t.Errorf("output = %q, want it to contain the marker content", output)
	}
}

// A deliberately failing command is captured as failed, with its output.
func TestRunBackupJob_FailureCapturesExitCodeAndOutput(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	vol, err := cli.VolumeCreate(ctx, volume.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), vol.Name, true) })

	output, exitCode, err := m.RunBackupJob(ctx, 0, testImage, "echo oops-failing && exit 3",
		nil, map[string]string{vol.Name: "/data"}, time.Minute)
	if err != nil {
		t.Fatalf("RunBackupJob should report the failure via exit code, not an error: %v", err)
	}
	if exitCode != 3 {
		t.Errorf("exitCode = %d, want 3", exitCode)
	}
	if !strings.Contains(output, "oops-failing") {
		t.Errorf("output = %q, want it to contain the command's output", output)
	}
}

// The helper container's env is passed through to the command.
func TestRunBackupJob_EnvPassedThrough(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	vol, err := cli.VolumeCreate(ctx, volume.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), vol.Name, true) })

	output, exitCode, err := m.RunBackupJob(ctx, 0, testImage, "echo $SECRET_VALUE",
		map[string]string{"SECRET_VALUE": "hunter2"}, map[string]string{vol.Name: "/data"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 || !strings.Contains(output, "hunter2") {
		t.Errorf("env var not passed through: exitCode=%d output=%q", exitCode, output)
	}
}

// The helper container is removed after the run, success or failure.
func TestRunBackupJob_RemovesHelperContainer(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	vol, err := cli.VolumeCreate(ctx, volume.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), vol.Name, true) })

	before, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.RunBackupJob(ctx, 0, testImage, "exit 1", nil, map[string]string{vol.Name: "/data"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	after, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("expected no leftover helper container: before=%d after=%d", len(before), len(after))
	}
}

// The declared timeout bounds the whole call end to end — create, start,
// wait AND the final log read — not just the wait step. A command that would
// otherwise run far longer than the timeout must still return promptly with
// a "timed out" error, exercising the same cctx from image-ensure through to
// ContainerLogs (previously the log read used an unbounded
// context.Background(), and the image pull ran before cctx even existed).
func TestRunBackupJob_TimeoutBoundsTheWholeCall(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	vol, err := cli.VolumeCreate(ctx, volume.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), vol.Name, true) })

	const timeout = 2 * time.Second
	start := time.Now()
	_, _, err = m.RunBackupJob(ctx, 0, testImage, "sleep 300",
		nil, map[string]string{vol.Name: "/data"}, timeout)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("RunBackupJob error = %v, want a timed-out error", err)
	}
	if elapsed > timeout+10*time.Second {
		t.Errorf("RunBackupJob took %s to report a timeout declared as %s — the call isn't actually bounded by it", elapsed, timeout)
	}
}

// ProjectVolumeNames resolves only the volumes Docker labeled for a given
// compose project, not others on the same host (including one whose NAME
// looks related but carries no compose label at all).
func TestProjectVolumeNames_ScopedToLabel(t *testing.T) {
	m, ctx := newManager(t)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	const project = "dc-backupjob-test-project"
	mine, err := cli.VolumeCreate(ctx, volume.CreateOptions{
		Labels: map[string]string{labelComposeProject: project},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), mine.Name, true) })

	other, err := cli.VolumeCreate(ctx, volume.CreateOptions{
		Labels: map[string]string{labelComposeProject: "dc-backupjob-test-other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), other.Name, true) })

	unlabeled, err := cli.VolumeCreate(ctx, volume.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), unlabeled.Name, true) })

	names, err := m.ProjectVolumeNames(ctx, 0, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != mine.Name {
		t.Fatalf("ProjectVolumeNames = %v, want only [%s]", names, mine.Name)
	}
}
