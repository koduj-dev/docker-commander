//go:build !windows

package docker

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// PENTEST-adjacent: the compose path comes from a container label, which anyone
// who can start a container chooses. A FIFO named like a compose file must not
// be opened: the open would block until a writer appears, hanging the check and
// the diagnostics request with it.
func TestStackFileAccess_FIFOIsNeverOpened(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yml")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	done := make(chan StackFileAccess, 1)
	go func() { done <- stackFileAccess(&Stack{Project: "trap", ConfigFile: p, WorkingDir: dir}) }()
	select {
	case a := <-done:
		if a.Readable || !strings.Contains(a.Reason, "isn't a regular file") {
			t.Errorf("got %+v, want refused as not a regular file", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the check blocked opening a FIFO")
	}
}
