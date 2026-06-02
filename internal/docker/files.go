package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// FileEntry is one item in a container directory listing.
type FileEntry struct {
	Name   string `json:"name"`
	IsDir  bool   `json:"isDir"`
	IsLink bool   `json:"isLink"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	Target string `json:"target,omitempty"` // symlink target
}

// execCapture runs a one-shot command in the container and returns its stdout,
// stderr and exit code. Used for filesystem operations the Docker API does not
// expose directly (listing, deleting). Requires the command to exist in the
// image (e.g. busybox/coreutils `ls`, `rm`).
func execCapture(ctx context.Context, cli *client.Client, id string, cmd []string) (string, string, int, error) {
	created, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd: cmd, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return "", "", 0, err
	}
	att, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", 0, err
	}
	defer att.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil {
		return "", "", 0, err
	}
	insp, err := cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return stdout.String(), stderr.String(), 0, err
	}
	return stdout.String(), stderr.String(), insp.ExitCode, nil
}

// ListPath lists one directory level inside a container by running `ls`. It is
// shell-independent (direct argv) but needs an `ls` binary in the image.
func (m *Manager) ListPath(ctx context.Context, hostID int64, id, path string) ([]FileEntry, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if path == "" {
		path = "/"
	}
	stdout, stderr, code, err := execCapture(ctx, cli, id, []string{"ls", "-lAp", path})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "cannot list directory (is `ls` present in the image?)"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return parseLsLong(stdout), nil
}

// parseLsLong parses `ls -lAp` output. Columns: mode nlink owner group size
// <date…> name, where -p suffixes directories with '/'. Names with spaces are
// preserved by joining the trailing tokens.
func parseLsLong(out string) []FileEntry {
	var entries []FileEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		mode := fields[0]
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		name := strings.Join(fields[8:], " ")

		e := FileEntry{Mode: mode, Size: size}
		switch mode[0] {
		case 'd':
			e.IsDir = true
		case 'l':
			e.IsLink = true
			if i := strings.Index(name, " -> "); i >= 0 {
				e.Target = name[i+4:]
				name = name[:i]
			}
		}
		// -p appends '/' to directory names.
		if strings.HasSuffix(name, "/") {
			e.IsDir = true
			name = strings.TrimSuffix(name, "/")
		}
		if name == "" {
			continue
		}
		e.Name = name
		entries = append(entries, e)
	}
	return entries
}

// CopyFrom streams a path out of a container as a TAR archive along with its
// stat (so the caller can tell a file from a directory). Caller closes the reader.
func (m *Manager) CopyFrom(ctx context.Context, hostID int64, id, path string) (io.ReadCloser, container.PathStat, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, container.PathStat{}, err
	}
	return cli.CopyFromContainer(ctx, id, path)
}

// CopyTo writes a TAR archive into the container at destDir.
func (m *Manager) CopyTo(ctx context.Context, hostID int64, id, destDir string, content io.Reader) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.CopyToContainer(ctx, id, destDir, content, container.CopyToContainerOptions{})
}

// DeletePath removes a path inside the container (rm -rf).
func (m *Manager) DeletePath(ctx context.Context, hostID int64, id, path string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	_, stderr, code, err := execCapture(ctx, cli, id, []string{"rm", "-rf", path})
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "delete failed"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
