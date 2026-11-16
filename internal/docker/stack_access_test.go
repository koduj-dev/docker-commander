package docker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stackIn writes a compose file into a fresh directory and returns a Stack
// whose labels point at it, as compose would record them.
func stackIn(t *testing.T, name string) (*Stack, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Stack{Project: "demo", ConfigFile: p, WorkingDir: dir}, dir
}

func needsPermissionDenial(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("access(2)-based checks are a no-op on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod-based permission denial doesn't apply")
	}
}

func TestStackFileAccess_ReadableAndWritable(t *testing.T) {
	st, dir := stackIn(t, "compose.yml")
	// Checking must not write next to the operator's compose file: pin the
	// directory's mtime and compare after.
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatal(err)
	}
	a := stackFileAccess(st)
	if !a.Readable || !a.Writable || a.Reason != "" {
		t.Fatalf("got %+v, want readable and writable with no reason", a)
	}
	if a.Path != st.ConfigFile || a.Project != "demo" {
		t.Errorf("path/project = %q/%q", a.Path, a.Project)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.ModTime().Equal(past) {
		t.Error("the check wrote into the stack's directory")
	}
}

func TestStackFileAccess_ReadOnlyDirectory(t *testing.T) {
	needsPermissionDenial(t)
	st, dir := stackIn(t, "compose.yml")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	a := stackFileAccess(st)
	if !a.Readable || a.Writable {
		t.Fatalf("got %+v, want readable but not writable", a)
	}
	if !strings.Contains(a.Reason, "has no access") && !strings.Contains(a.Reason, "ProtectHome") {
		t.Errorf("reason = %q, want the access hint", a.Reason)
	}
}

func TestStackFileAccess_Unreadable(t *testing.T) {
	needsPermissionDenial(t)
	st, _ := stackIn(t, "compose.yml")
	if err := os.Chmod(st.ConfigFile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(st.ConfigFile, 0o644) })
	a := stackFileAccess(st)
	if a.Readable || a.Writable {
		t.Fatalf("got %+v, want unreadable", a)
	}
	if !strings.Contains(a.Reason, "permission denied") || !strings.Contains(a.Reason, "Docker Commander runs as") {
		t.Errorf("reason = %q, want the error plus who it runs as", a.Reason)
	}
}

func TestStackFileAccess_MissingFile(t *testing.T) {
	st, _ := stackIn(t, "compose.yml")
	if err := os.Remove(st.ConfigFile); err != nil {
		t.Fatal(err)
	}
	a := stackFileAccess(st)
	if a.Readable {
		t.Fatal("a missing file can't be readable")
	}
	if a.Path != st.ConfigFile {
		t.Errorf("path = %q, want the recorded path even when it doesn't open", a.Path)
	}
	if !strings.Contains(a.Reason, "isn't visible to the Docker Commander process") {
		t.Errorf("reason = %q, want the not-visible hint", a.Reason)
	}
}

func TestStackFileAccess_NonYAMLIsNeverOpened(t *testing.T) {
	st, _ := stackIn(t, "secrets.env")
	a := stackFileAccess(st)
	if a.Readable || !strings.Contains(a.Reason, ".yml/.yaml") {
		t.Errorf("got %+v, want a non-YAML path refused like resolveStack does", a)
	}
}
