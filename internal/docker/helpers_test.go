package docker

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestRegistryHost(t *testing.T) {
	cases := map[string]string{
		"nginx":                      "docker.io",
		"nginx:latest":               "docker.io",
		"library/nginx:1.27":         "docker.io",
		"user/app:tag":               "docker.io",
		"ghcr.io/owner/app:tag":      "ghcr.io",
		"registry.example.com/a/b":   "registry.example.com",
		"localhost:5000/app:dev":     "localhost:5000",
		"quay.io/org/img@sha256:abc": "quay.io",
	}
	for ref, want := range cases {
		if got := registryHost(ref); got != want {
			t.Errorf("registryHost(%q) = %q want %q", ref, got, want)
		}
	}
}

func TestEncodeAuth(t *testing.T) {
	enc, err := encodeAuth(&store.RegistryAuth{Address: "ghcr.io", Username: "alice", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.URLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("not valid base64url: %v", err)
	}
	if !strings.Contains(string(raw), "alice") || !strings.Contains(string(raw), "ghcr.io") {
		t.Errorf("encoded auth missing fields: %s", raw)
	}
}

func TestIsDangling(t *testing.T) {
	if !isDangling(nil) || !isDangling([]string{}) || !isDangling([]string{"<none>:<none>"}) {
		t.Error("untagged images should be dangling")
	}
	if isDangling([]string{"nginx:latest"}) {
		t.Error("tagged image should not be dangling")
	}
}

func TestChangeKind(t *testing.T) {
	if changeKind(container.ChangeModify) != "modified" ||
		changeKind(container.ChangeAdd) != "added" ||
		changeKind(container.ChangeDelete) != "deleted" {
		t.Error("change kind mapping wrong")
	}
}

func TestFlattenEvent(t *testing.T) {
	e := events.Message{
		Type:   "container",
		Action: "die",
		Time:   123,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web"}},
	}
	m := flattenEvent(e)
	if m.Type != "container" || m.Action != "die" || m.ID != "abc123" || m.Name != "web" || m.Time != 123 {
		t.Errorf("flattenEvent wrong: %+v", m)
	}
	// Falls back to the image attribute when there's no name.
	img := flattenEvent(events.Message{Type: "image", Action: "pull", Actor: events.Actor{Attributes: map[string]string{"image": "nginx"}}})
	if img.Name != "nginx" {
		t.Errorf("expected image fallback, got %q", img.Name)
	}
}

func TestParseSSHAddress(t *testing.T) {
	u, hp, err := parseSSHAddress("deploy@server")
	if err != nil || u != "deploy" || hp != "server:22" {
		t.Errorf("default port: got (%q,%q,%v)", u, hp, err)
	}
	u, hp, _ = parseSSHAddress("ssh://root@host:2222")
	if u != "root" || hp != "host:2222" {
		t.Errorf("explicit port/scheme: got (%q,%q)", u, hp)
	}
	if _, _, err := parseSSHAddress("no-at-sign"); err == nil {
		t.Error("missing user@ should error")
	}
	if _, _, err := parseSSHAddress("@host"); err == nil {
		t.Error("empty user should error")
	}
}

func TestParseLsLong(t *testing.T) {
	// Default `ls -lAp` time style is "Mon DD HH:MM" (3 tokens), so the name
	// starts at field index 8.
	out := strings.Join([]string{
		"total 20",
		"drwxr-xr-x  2 root root 4096 Jun  2 10:00 bin/",
		"-rw-r--r--  1 root root  123 Jun  2 10:00 hosts",
		"lrwxrwxrwx  1 root root    7 Jun  2 10:00 link -> target",
		"-rw-r--r--  1 root root   42 Jun  2 10:00 file with spaces.txt",
		"",
	}, "\n")
	entries := parseLsLong(out)
	byName := map[string]FileEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if d, ok := byName["bin"]; !ok || !d.IsDir {
		t.Error("bin should be a directory")
	}
	if f, ok := byName["hosts"]; !ok || f.IsDir || f.Size != 123 {
		t.Errorf("hosts should be a 123-byte file: %+v", f)
	}
	if l, ok := byName["link"]; !ok || !l.IsLink || l.Target != "target" {
		t.Errorf("link should be a symlink to target: %+v", l)
	}
	if _, ok := byName["file with spaces.txt"]; !ok {
		t.Error("names with spaces should be preserved")
	}
	if _, ok := byName["total"]; ok {
		t.Error("the 'total' header line must be skipped")
	}
}
