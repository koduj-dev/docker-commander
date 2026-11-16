//go:build !windows

package docker

import (
	"os"
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

// Creating a file in a directory needs search (x) permission as well as write.
// A 0222 directory passes a W_OK-only access(2) and still refuses the create.
func TestPathWritable_DirectoryNeedsExecute(t *testing.T) {
	needsPermissionDenial(t)
	dir := filepath.Join(t.TempDir(), "noexec")
	if err := os.Mkdir(dir, 0o222); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := PathWritable(dir); err == nil {
		t.Error("a write-only directory (no x) was reported writable")
	}
	if err := dirWritable(dir); err == nil {
		t.Error("dirWritable (stack Save precheck) reported a write-only directory writable")
	}
	// A plain writable file is writable without any x bit.
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PathWritable(f); err != nil {
		t.Errorf("writable file: %v", err)
	}
}
