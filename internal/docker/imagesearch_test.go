package docker

import "testing"

func TestHubRepoPath(t *testing.T) {
	cases := []struct {
		in       string
		wantRepo string
		wantOK   bool
	}{
		{"nginx", "library/nginx", true},
		{"NGINX", "library/nginx", true},
		{"library/nginx", "library/nginx", true},
		{"bitnami/redis", "bitnami/redis", true},
		{"nginx:alpine", "library/nginx", true},
		{"nginx@sha256:abc", "library/nginx", true},
		{"docker.io/library/nginx", "library/nginx", true},
		{"ghcr.io/foo/bar", "", false},       // non-Hub registry
		{"localhost:5000/app", "", false},    // local registry
		{"registry.io/x:1", "", false},       // explicit host
		{"", "", false},                      // empty
		{"../../etc/passwd", "", false},      // not a valid repo path
		{"foo/bar/baz", "foo/bar/baz", true}, // nested path still valid charset
	}
	for _, c := range cases {
		repo, ok := hubRepoPath(c.in)
		if ok != c.wantOK || repo != c.wantRepo {
			t.Errorf("hubRepoPath(%q) = (%q, %v); want (%q, %v)", c.in, repo, ok, c.wantRepo, c.wantOK)
		}
	}
}
