package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingLogAppendsAndRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dockercmd.log")
	if err := os.WriteFile(path, []byte("from the last run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := openRotatingLog(path, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	for _, line := range []string{"first line ....\n", "second line ...\n", "third line ....\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	old, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("no rotated copy: %v", err)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(old), "from the last run\nfirst line") {
		t.Errorf("the rotated copy lost what was there before: %q", old)
	}
	if string(cur) != "third line ....\n" {
		t.Errorf("current file = %q, want only the line written after rotating", cur)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("log mode %v, want owner-only", fi.Mode().Perm())
	}
}
