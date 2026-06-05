package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()

	ok := []string{"compose.yml", "config/app.conf", "scripts/run.sh", "a/b/c.txt", "a/../b.txt"}
	for _, name := range ok {
		full, err := safeJoin(root, name)
		if err != nil {
			t.Errorf("%q should be allowed: %v", name, err)
			continue
		}
		if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
			t.Errorf("%q resolved outside root: %s", name, full)
		}
	}

	bad := []string{"", "  ", "../escape", "../../etc/passwd", "/etc/passwd", "a/../../b"}
	for _, name := range bad {
		if _, err := safeJoin(root, name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}

	// A symlink target is rejected (escape guard).
	link := filepath.Join(root, "link.yml")
	if err := os.Symlink("/etc/hostname", link); err == nil {
		if _, err := safeJoin(root, "link.yml"); err == nil {
			t.Error("symlink should be rejected")
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My App":         "my-app",
		"  Web  Stack  ": "web-stack",
		"a__b--c":        "a-b-c",
		"9lives":         "9lives",
		"-leading-":      "leading",
		"!!!":            "project",
		"Foo.Bar":        "foo-bar",
		"Další projekt":  "dalsi-projekt", // diacritics transliterated, not dropped
		"Žluťoučký":      "zlutoucky",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
