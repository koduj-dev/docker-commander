package docker

import (
	"context"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// sshProbeMaxOutput bounds how much of a probe command's output is kept.
// `ip`/`ifconfig`/`df` output is normally a few KB; 256KB is generous headroom
// while still bounding what a hostile or compromised remote host could throw
// at us — sshExec's session output is not otherwise size-limited.
const sshProbeMaxOutput = 256 * 1024

// boundedWriter is an io.Writer that keeps only the first max bytes written to
// it. Write always reports success and the full length written, even past the
// cap: it exists to sit on an SSH session's Stdout/Stderr, and a Write that
// errors or short-returns would stall the session's read loop — the remote
// command would hang instead of the extra bytes just being discarded.
type boundedWriter struct {
	buf []byte
	max int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room > 0 {
		n := len(p)
		if n > room {
			n = room
		}
		w.buf = append(w.buf, p[:n]...)
	}
	return len(p), nil
}

func (w *boundedWriter) String() string { return string(w.buf) }

// sshExec runs a single command on hostID's remote shell over the cached SSH
// connection and returns its bounded combined output. It is the general-purpose
// counterpart to stacks_edit.go's sshRun/sshRunStdin, which are scoped to a
// *stackTarget and used for compose operations; this one is keyed directly on
// (hostID, *store.Host) for the diagnostics probes, which have no stack.
func (m *Manager) sshExec(ctx context.Context, hostID int64, h *store.Host, cmd string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cli, err := m.sshClientFor(hostID, h)
	if err != nil {
		return "", err
	}
	sess, err := cli.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	out := &boundedWriter{max: sshProbeMaxOutput}
	sess.Stdout = out
	sess.Stderr = out

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case err := <-done:
		return out.String(), err
	case <-ctx.Done():
		// Closing the session unblocks the goroutine; without this a command
		// that hangs on the remote end would leak the goroutine for good.
		_ = sess.Close()
		return "", ctx.Err()
	}
}
