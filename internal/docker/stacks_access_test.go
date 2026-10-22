package docker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// A compose file the server process can't open used to surface as a bare
// "open …: permission denied". These pin the hint that names the cause, and that
// the original error stays reachable through errors.Is.

func TestLocalAccessHint(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    []string
		notWant []string
		is      error
	}{
		{
			name: "permission denied under /home names ProtectHome",
			err:  &fs.PathError{Op: "open", Path: "/home/filip/apps/x/docker-compose.yml", Err: syscall.EACCES},
			want: []string{"permission denied", "ProtectHome=true", "docs/deployment.md"},
			is:   fs.ErrPermission,
		},
		{
			name: "permission denied under /root names ProtectHome",
			err:  &fs.PathError{Op: "open", Path: "/root/app/compose.yml", Err: syscall.EACCES},
			want: []string{"ProtectHome=true"},
			is:   fs.ErrPermission,
		},
		{
			name:    "permission denied elsewhere asks for access, not ProtectHome",
			err:     &fs.PathError{Op: "open", Path: "/srv/app/compose.yml", Err: syscall.EACCES},
			want:    []string{"has no access", "read access"},
			notWant: []string{"ProtectHome"},
			is:      fs.ErrPermission,
		},
		{
			name:    "a /homeless path is not a home directory",
			err:     &fs.PathError{Op: "open", Path: "/homeless/compose.yml", Err: syscall.EACCES},
			notWant: []string{"ProtectHome"},
			is:      fs.ErrPermission,
		},
		{
			name: "read-only file system names ProtectSystem",
			err:  &fs.PathError{Op: "open", Path: "/srv/app/.dc-compose-1.yml", Err: syscall.EROFS},
			want: []string{"ProtectSystem=strict", "ReadWritePaths"},
			is:   syscall.EROFS,
		},
		{
			name: "missing file hints at a container without the bind mount",
			err:  &fs.PathError{Op: "open", Path: "/srv/app/compose.yml", Err: syscall.ENOENT},
			want: []string{"bind-mount"},
			is:   fs.ErrNotExist,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := localAccessHint(c.err)
			for _, w := range c.want {
				if !strings.Contains(got.Error(), w) {
					t.Errorf("error %q lacks %q", got, w)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(got.Error(), w) {
					t.Errorf("error %q should not mention %q", got, w)
				}
			}
			if !errors.Is(got, c.is) {
				t.Errorf("errors.Is(%v, %v) = false; the original error must stay wrapped", got, c.is)
			}
		})
	}

	t.Run("non-path errors pass through unchanged", func(t *testing.T) {
		e := errors.New("boom")
		if got := localAccessHint(e); got != e {
			t.Errorf("got %v, want the same error back", got)
		}
	})
}

// TestReadHostFileUnreadableExplains goes through the real local read path with a
// file this process can't open.
func TestReadHostFileUnreadableExplains(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file anyway")
	}
	p := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(p, []byte("services: {}\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	m := &Manager{}
	_, err := m.readHostFile(context.Background(), 1, &store.Host{Kind: "local"}, p)
	if err == nil {
		t.Fatal("reading a 0000 file succeeded")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("got %v, want a permission error", err)
	}
	if !strings.Contains(err.Error(), "Docker Commander runs as") {
		t.Errorf("error %q carries no hint about the service user", err)
	}
}

// TestWriteLocalUnwritableExplains: saving next to a compose file in a directory
// this process can't write to carries the same hint.
func TestWriteLocalUnwritableExplains(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a 0555 directory anyway")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(p, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	tg := &stackTarget{stack: &Stack{Project: "x"}, path: p, workDir: dir}
	err := tg.writeLocal(context.Background(), "services: {}\n")
	if err == nil {
		t.Fatal("writing into a 0555 directory succeeded")
	}
	if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "Docker Commander runs as") {
		t.Errorf("error %q carries no hint about the service user", err)
	}
}
