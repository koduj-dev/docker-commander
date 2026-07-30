package api

import (
	"context"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestJoinBinds(t *testing.T) {
	got := joinBinds([]docker.ProjectBind{
		{Service: "web", Source: "/etc/localtime", Target: "/etc/localtime"},
		{Service: "db", Source: "/var/run/docker.sock", Target: "/sock"},
	})
	for _, want := range []string{"web", "/etc/localtime", "db", "/var/run/docker.sock"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q is missing %q — the user can't tell which mount to fix", got, want)
		}
	}
	if strings.Count(got, ";") != 1 {
		t.Errorf("binds should be separated once per extra entry: %q", got)
	}
}

func TestJoinBinds_Empty(t *testing.T) {
	if got := joinBinds(nil); got != "" {
		t.Errorf("no binds should render empty, got %q", got)
	}
}

// The note is the only place the user learns that a remote deploy copies files
// instead of mounting them live, so it must name the paths and say "snapshot".
func TestRemoteBindNote(t *testing.T) {
	note := remoteBindNote([]docker.ProjectBind{
		{Service: "web", Rel: "html", Target: "/usr/share/nginx/html"},
		{Service: "web", Rel: "nginx.conf", Target: "/etc/nginx/nginx.conf"},
	}, nil)
	for _, want := range []string{"html", "nginx.conf", "snapshot", "redeploy"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q should mention %q", note, want)
		}
	}
	if !strings.Contains(note, "2 bind") {
		t.Errorf("note should count the paths: %q", note)
	}
}

// Two services sharing one bind source are one copied path, not two.
func TestRemoteBindNote_DeduplicatesPaths(t *testing.T) {
	note := remoteBindNote([]docker.ProjectBind{
		{Service: "web", Rel: "shared", Target: "/a"},
		{Service: "api", Rel: "shared", Target: "/b"},
	}, nil)
	if !strings.Contains(note, "1 bind") {
		t.Errorf("a shared source should count once: %q", note)
	}
	if strings.Count(note, "shared") != 1 {
		t.Errorf("path should be listed once: %q", note)
	}
}

// When a project opted into host paths, the note must say so loudly and name the
// mounts — this is the one case where a deploy mounts something we never saw.
func TestRemoteBindNote_PassedThroughHostPaths(t *testing.T) {
	note := remoteBindNote(nil, []docker.ProjectBind{
		{Service: "web", Source: "/etc/localtime", Target: "/etc/localtime"},
	})
	for _, want := range []string{"WARNING", "/etc/localtime", "REMOTE", "nothing was copied"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q should mention %q", note, want)
		}
	}
}

// Both kinds at once: copied paths and passed-through host paths.
func TestRemoteBindNote_BothKinds(t *testing.T) {
	note := remoteBindNote(
		[]docker.ProjectBind{{Service: "web", Rel: "html", Target: "/usr/share/nginx/html"}},
		[]docker.ProjectBind{{Service: "web", Source: "/var/run/docker.sock", Target: "/sock"}},
	)
	if !strings.Contains(note, "Copied 1 bind") {
		t.Errorf("note should report the copied path: %q", note)
	}
	if !strings.Contains(note, "/var/run/docker.sock") {
		t.Errorf("note should report the passed-through path: %q", note)
	}
	// The copied part leads; the warning follows, separated as its own paragraph.
	if !strings.Contains(note, "\n\nWARNING") {
		t.Errorf("the warning should be its own paragraph: %q", note)
	}
}

// Nothing to report must be empty, not a note claiming zero paths.
func TestRemoteBindNote_EmptyWhenNothingHappened(t *testing.T) {
	if got := remoteBindNote(nil, nil); got != "" {
		t.Errorf("expected no note, got %q", got)
	}
}

// A local project must not go anywhere near the seeding path: no override files,
// no note, and no attempt to reach a Docker host.
func TestProjectDeployEnv_LocalIsUntouched(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{cfg: config.Config{}, store: st}

	p := &store.Project{ID: 1, Slug: "demo", ComposeFile: "compose.yml", HostID: 0}
	env, files, note, cleanup, err := srv.projectDeployEnv(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatalf("local deploy should not error: %v", err)
	}
	defer cleanup()
	if env != nil {
		t.Errorf("local deploy needs no env overrides, got %v", env)
	}
	if len(files) != 0 {
		t.Errorf("local deploy needs no override files, got %v", files)
	}
	if note != "" {
		t.Errorf("local deploy should produce no note, got %q", note)
	}
}

// A project pointing at a host row that no longer exists must fail with a
// message that tells the user how to fix it, not a bare store error.
func TestProjectDeployEnv_MissingHost(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{cfg: config.Config{}, store: st}

	p := &store.Project{ID: 1, Slug: "demo", ComposeFile: "compose.yml", HostID: 4242}
	_, _, _, cleanup, err := srv.projectDeployEnv(context.Background(), p, t.TempDir())
	cleanup() // must be safe even on the error path
	if err == nil {
		t.Fatal("a project whose target host is gone should not deploy")
	}
	if !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("error should point at the missing host, got %v", err)
	}
}

// A disabled host is skipped everywhere else, so a deploy must refuse it too
// rather than silently falling back to the local daemon.
func TestProjectDeployEnv_DisabledHostRefused(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.CreateHost(ctx, &store.Host{
		Name: "edge", Kind: "tcp", Address: "tcp://10.0.0.9:2376",
	})
	if err != nil {
		t.Fatal(err)
	}
	// CreateHost doesn't take the flag; disabling is its own operation.
	if err := st.SetHostDisabled(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: config.Config{}, store: st}

	p := &store.Project{ID: 1, Slug: "demo", ComposeFile: "compose.yml", HostID: id}
	_, _, _, cleanup, err := srv.projectDeployEnv(ctx, p, t.TempDir())
	cleanup()
	if err == nil {
		t.Fatal("a disabled host should not be deployed to")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should say the host is disabled, got %v", err)
	}
}
