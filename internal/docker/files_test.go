package docker

import "testing"

func TestArchiveEntryName(t *testing.T) {
	ok := map[string]string{
		"compose.yml":       "compose.yml",
		"config/app.conf":   "config/app.conf",
		"a/b/c.txt":         "a/b/c.txt",
		"./foo.txt":         "foo.txt",
		"dir\\win.txt":      "dir/win.txt", // backslashes normalised
		"a/./b/../b/x.txt":  "a/b/x.txt",   // cleaned, still inside
		"foo..bar":          "foo..bar",    // ".." only inside a name is fine
		"data/2024..backup": "data/2024..backup",
	}
	for in, want := range ok {
		got, valid := archiveEntryName(in)
		if !valid {
			t.Errorf("%q should be allowed", in)
			continue
		}
		if got != want {
			t.Errorf("archiveEntryName(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{"", "  ", "/etc/passwd", "../escape", "../../etc/passwd", "a/../../b", "..", "foo/../../bar"}
	for _, in := range bad {
		if got, valid := archiveEntryName(in); valid {
			t.Errorf("%q should be rejected (got %q)", in, got)
		}
	}
}
