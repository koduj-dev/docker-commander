package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"
)

// Volume backup jobs are a trigger-and-status wrapper around a user-supplied
// command (their own restic/borg/etc, already pointed at its own repository),
// run against a volume's or project's data via a short-lived helper
// container — reusing the exact pattern volumefiles.go established for
// working with a named volume uniformly across local/SSH/TCP+TLS hosts. Not a
// backup engine: no repository, retention or storage logic of our own.

const (
	backupJobLabel  = "dc.backupjob"
	maxBackupOutput = 1 << 20 // 1MiB captured output ceiling
)

// backupSem bounds concurrent backup-job helper containers so a burst of
// scheduled/manual runs can't be fan-fired to exhaust a host.
var backupSem = make(chan struct{}, 2)

// ProjectVolumeNames returns the named volumes Docker Compose actually
// created for a project, found via the authoritative
// com.docker.compose.project label — not guessed from the compose file's
// declared (unprefixed) volume names, which compose runtime-prefixes.
func (m *Manager) ProjectVolumeNames(ctx context.Context, hostID int64, project string) ([]string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	list, err := cli.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", labelComposeProject+"="+project)),
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Volumes))
	for _, v := range list.Volumes {
		names = append(names, v.Name)
	}
	return names, nil
}

// RunBackupJob runs command (as `sh -c <command>`) inside a throwaway helper
// container with mounts (volume name -> mount path) bind-mounted, capturing
// combined stdout/stderr output and the exit code. The container is created,
// started, waited on and removed unconditionally — nothing about it persists.
//
// Running the user's command inside a container, not via host SSH exec, is
// the deliberate design boundary: an admin configuring a backup job already
// has full Docker API control (can already run anything via the existing
// container-create flows), so this adds no new class of capability — just a
// convenient, scheduled packaging of one that exists, contained the way a raw
// host shell command would not be.
func (m *Manager) RunBackupJob(ctx context.Context, hostID int64, image, command string, env map[string]string, mounts map[string]string, timeout time.Duration) (output string, exitCode int, err error) {
	select {
	case backupSem <- struct{}{}:
		defer func() { <-backupSem }()
	default:
		return "", 0, errors.New("too many backup jobs running — try again shortly")
	}

	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", 0, err
	}

	// The declared timeout must bound the whole synchronous flow, including
	// the image pull and the final log read — not just create/start/wait —
	// otherwise a stuck pull or a stuck log stream can hold the worker and
	// backupSem slot far longer than the caller asked for.
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := ensureHelperImage(cctx, cli, image); err != nil {
		return "", 0, fmt.Errorf("pull image: %w", err)
	}

	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	mountList := make([]mount.Mount, 0, len(mounts))
	for volName, target := range mounts {
		mountList = append(mountList, mount.Mount{Type: mount.TypeVolume, Source: volName, Target: target})
	}

	resp, err := cli.ContainerCreate(cctx,
		&container.Config{
			Image:  image,
			Cmd:    []string{"sh", "-c", command},
			Env:    envList,
			Labels: map[string]string{backupJobLabel: "1"},
		},
		&container.HostConfig{Mounts: mountList}, nil, nil, "")
	if err != nil {
		return "", 0, fmt.Errorf("create helper: %w", err)
	}
	// Best-effort cleanup with its own context: cctx may already be past its
	// deadline (a timed-out run must still remove the container it created).
	defer func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rcancel()
		_ = cli.ContainerRemove(rctx, resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := cli.ContainerStart(cctx, resp.ID, container.StartOptions{}); err != nil {
		return "", 0, fmt.Errorf("start helper: %w", err)
	}

	statusCh, errCh := cli.ContainerWait(cctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		if werr != nil {
			if errors.Is(cctx.Err(), context.DeadlineExceeded) {
				return "", 0, fmt.Errorf("backup job timed out after %s", timeout)
			}
			return "", 0, fmt.Errorf("wait for helper: %w", werr)
		}
	case status := <-statusCh:
		exitCode = int(status.StatusCode)
	}

	logs, err := cli.ContainerLogs(cctx, resp.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", exitCode, fmt.Errorf("read output: %w", err)
	}
	defer logs.Close()

	buf := &capBuffer{cap: maxBackupOutput}
	_, _ = stdcopy.StdCopy(buf, buf, logs) // best-effort: a truncated capture still reports the exit code
	return buf.buf.String(), exitCode, nil
}
