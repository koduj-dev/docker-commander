package docker

import "testing"

// volPath must keep every resolved path inside the helper's /data mount, even
// for traversal/absolute inputs (they're jailed, not allowed to escape).
func TestVolPath(t *testing.T) {
	cases := map[string]string{
		"":                 "/data",
		"/":                "/data",
		"foo.txt":          "/data/foo.txt",
		"a/b/c":            "/data/a/b/c",
		"  spaced ":        "/data/spaced",
		"../escape":        "/data/escape",
		"../../etc/passwd": "/data/etc/passwd",
		"/abs/path":        "/data/abs/path",
		"a/../b":           "/data/b",
	}
	for in, want := range cases {
		if got := volPath(in); got != want {
			t.Errorf("volPath(%q) = %q, want %q", in, got, want)
		}
	}
}
