package api

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// swapInEscapingLink checks "sub/<file>" with safeJoin while sub is still a
// real folder, then replaces sub with a symlink to victim — the window
// between the check and the use that a container with the project folder
// mounted could race. It returns the already-approved path.
func swapInEscapingLink(t *testing.T, root, victim, file string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	full, err := safeJoin(root, "sub/"+file)
	if err != nil {
		t.Fatalf("safeJoin refused a plain path: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	return full
}

// PENTEST: a path safeJoin approved must not reach outside the sandbox when
// a symlink is swapped in before the write, read, delete or mkdir runs.
func TestPentestRootIOIgnoresSymlinkSwappedInAfterCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}

	t.Run("write", func(t *testing.T) {
		root, victim := t.TempDir(), t.TempDir()
		full := swapInEscapingLink(t, root, victim, "pwn.txt")
		if err := writeInRoot(root, full, []byte("PWNED"), projectDirMode, projectFileMode); err == nil {
			t.Error("SECURITY: write through a swapped-in symlink succeeded")
		}
		if _, err := os.Stat(filepath.Join(victim, "pwn.txt")); err == nil {
			t.Fatal("SECURITY: write landed outside the sandbox")
		}
	})

	t.Run("read", func(t *testing.T) {
		root, victim := t.TempDir(), t.TempDir()
		mustWriteFile(t, filepath.Join(victim, "secret.txt"), "SECRET")
		full := swapInEscapingLink(t, root, victim, "secret.txt")
		if f, err := openInRoot(root, full); err == nil {
			f.Close()
			t.Fatal("SECURITY: read through a swapped-in symlink succeeded")
		}
	})

	t.Run("delete", func(t *testing.T) {
		root, victim := t.TempDir(), t.TempDir()
		mustWriteFile(t, filepath.Join(victim, "keep.txt"), "KEEP")
		full := swapInEscapingLink(t, root, victim, "keep.txt")
		_ = removeInRoot(root, full)
		if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
			t.Fatal("SECURITY: delete through a swapped-in symlink removed a file outside the sandbox")
		}
	})

	t.Run("mkdir", func(t *testing.T) {
		root, victim := t.TempDir(), t.TempDir()
		full := swapInEscapingLink(t, root, victim, "newdir")
		_ = mkdirInRoot(root, full, projectDirMode)
		if _, err := os.Stat(filepath.Join(victim, "newdir")); err == nil {
			t.Fatal("SECURITY: mkdir through a swapped-in symlink created a folder outside the sandbox")
		}
	})
}

// The Root helpers still do the ordinary job: nested folders are created,
// a file reads back, delete removes it, and a symlink that stays inside the
// sandbox keeps working.
func TestRootIOInsideSandbox(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project") // not created yet
	full := filepath.Join(root, "a", "b", "c.yml")
	if err := writeInRoot(root, full, []byte("x: 1"), projectDirMode, projectFileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	if missingInRoot(root, full) {
		t.Error("missingInRoot says a written file is missing")
	}
	f, err := openInRoot(root, full)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Close()
	if err := removeInRoot(root, full); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !missingInRoot(root, full) {
		t.Error("missingInRoot says a removed file is there")
	}
	if err := mkdirInRoot(root, filepath.Join(root, "x", "y"), projectDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("x", filepath.Join(root, "inner")); err != nil {
			t.Fatal(err)
		}
		if err := writeInRoot(root, filepath.Join(root, "inner", "y", "z.txt"), []byte("ok"), projectDirMode, projectFileMode); err != nil {
			t.Errorf("write through an in-sandbox symlink: %v", err)
		}
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
