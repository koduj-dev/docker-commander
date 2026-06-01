package docker

import (
	"context"
	"net"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// defaultShell tries bash, falling back to sh, so the terminal works across
// both full and minimal (alpine, distroless-ish) images.
var defaultShell = []string{"/bin/sh", "-c", "[ -x /bin/bash ] && exec /bin/bash || exec /bin/sh"}

// ExecSession is a live interactive exec attached to a container. It is a
// bidirectional byte stream (Read = container output, Write = stdin) plus a
// Resize control. Works for any host kind, since it goes through the Docker API.
type ExecSession struct {
	resp   types.HijackedResponse
	client execResizer
	execID string
}

// execResizer is the slice of the docker client the session needs for resizing.
type execResizer interface {
	ContainerExecResize(ctx context.Context, execID string, options container.ResizeOptions) error
}

// ExecAttach starts a TTY exec in the container and attaches to it. Pass cmd to
// override the shell. cols/rows seed the initial terminal size.
func (m *Manager) ExecAttach(ctx context.Context, hostID int64, containerID string, cmd []string, cols, rows uint) (*ExecSession, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if len(cmd) == 0 {
		cmd = defaultShell
	}
	created, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  &[2]uint{rows, cols},
	})
	if err != nil {
		return nil, err
	}
	resp, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, err
	}
	return &ExecSession{resp: resp, client: cli, execID: created.ID}, nil
}

// Read returns container output (stdout+stderr merged, since TTY).
func (s *ExecSession) Read(p []byte) (int, error) { return s.resp.Reader.Read(p) }

// Write sends bytes to the exec's stdin.
func (s *ExecSession) Write(p []byte) (int, error) { return s.resp.Conn.Write(p) }

// Conn exposes the underlying connection (for setting deadlines if needed).
func (s *ExecSession) Conn() net.Conn { return s.resp.Conn }

// Resize updates the remote TTY dimensions.
func (s *ExecSession) Resize(ctx context.Context, cols, rows uint) error {
	return s.client.ContainerExecResize(ctx, s.execID, container.ResizeOptions{Height: rows, Width: cols})
}

// Close tears down the hijacked connection.
func (s *ExecSession) Close() { s.resp.Close() }
