package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// A stack that a Project deploys (Compose project = the project's slug, on the
// project's host) can't be edited or redeployed from Stacks: that would bypass
// the project's revisions and policy checks, and the next project deploy would
// undo it. The refusal comes before anything touches the host.
func TestStacksRefuseToEditAProjectsStack(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})
	ctx := context.Background()
	if _, err := a.st.CreateProject(ctx, &store.Project{Name: "Shop", Slug: "dctest-shop", ComposeFile: "compose.yml"}); err != nil {
		t.Fatal(err)
	}
	remote, err := a.st.CreateHost(ctx, &store.Host{Name: "b", Kind: "tcp", Address: "tcp://127.0.0.1:2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreateProject(ctx, &store.Project{Name: "Elsewhere", Slug: "dctest-elsewhere", ComposeFile: "compose.yml", HostID: remote}); err != nil {
		t.Fatal(err)
	}

	refused := func(method, path string, body any) bool {
		t.Helper()
		_, out := a.do(method, path, body)
		msg, _ := out["error"].(string)
		return out["ok"] == false && strings.Contains(msg, "belongs to the project")
	}
	if !refused("PUT", "/api/stacks/dctest-shop/compose", map[string]string{"content": "services: {}\n"}) {
		t.Error("saving the compose file of a project's stack was not refused")
	}
	if !refused("POST", "/api/stacks/dctest-shop/redeploy", nil) {
		t.Error("redeploying a project's stack from Stacks was not refused")
	}
	// Same slug, different host: on the local daemon this isn't the project's
	// stack, so the guard must not claim it (whatever else then fails).
	if refused("POST", "/api/stacks/dctest-elsewhere/redeploy", nil) {
		t.Error("a local stack was refused because a project on another host shares its name")
	}
	// A plain CLI stack is not affected by the guard.
	if refused("PUT", "/api/stacks/dctest-cli-stack/compose", map[string]string{"content": "services: {}\n"}) {
		t.Error("a CLI stack that no project owns was refused as a project's")
	}
}

// The compose file of a project's stack still opens in Stacks, read-only, with
// the reason. Needs a real stack, so it runs against the daemon.
func TestStacksShowAProjectsStackReadOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	ctx := context.Background()
	if !docker.ComposeAvailable(ctx) {
		t.Skip("docker compose CLI not available")
	}
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})
	const slug = "dctest-guarded-stack"
	if _, err := a.st.CreateProject(ctx, &store.Project{Name: "Guarded", Slug: slug, ComposeFile: "compose.yml"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fixed slug: clear what an interrupted run left behind, before and after.
	freeDeployStack(slug)
	t.Cleanup(func() {
		_, _ = docker.ComposeDown(context.Background(), dir, slug, nil)
		freeDeployStack(slug)
	})
	if out, err := docker.ComposeUpFiles(ctx, dir, slug, nil, nil, nil, false); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}

	_, out := a.do("GET", "/api/stacks/"+slug+"/compose", nil)
	if out["ok"] != true {
		t.Fatalf("the compose file should still open: %v", out)
	}
	reason, _ := out["readOnlyReason"].(string)
	if out["editable"] != false || !strings.Contains(reason, "Projects") {
		t.Fatalf("a project's stack must open read-only with the reason, got editable=%v reason=%q", out["editable"], reason)
	}
}

// When the projects can't be listed, the guard refuses rather than letting the
// edit through: not knowing whether a project owns the stack is not "no".
func TestManagedByProjectFailsClosed(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close() // every query now fails
	srv := &Server{store: st}
	if reason := srv.managedByProject(context.Background(), 0, "any-stack"); reason == "" {
		t.Fatal("SECURITY: a failed project lookup let the stack be edited")
	}
}
