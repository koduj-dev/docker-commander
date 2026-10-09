package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Deploying a managed project to a REMOTE host used to be refused over MCP
// outright. Now that it is allowed, the guard that made the web UI's remote
// deploy safe has to apply here too, and the failure mode is quiet: MCP and the
// UI resolve their compose environment through different helpers, and only one
// of them inspects bind mounts. Swapping back to the simpler helper would still
// deploy, still pass every other test, and silently mount paths off the remote
// host's filesystem.
//
// So this asserts the MCP path reaches the refusal, not just that the refusal
// exists.

// remoteProjectServer builds a server whose single project targets a remote host
// and whose compose file mounts a path from OUTSIDE the project folder.
func remoteProjectServer(t *testing.T, compose string) (*Server, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	hostID, err := st.CreateHost(ctx, &store.Host{Name: "edge", Kind: "tcp", Address: "tcp://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{
		Name: "shop", Slug: "shop", ComposeFile: "compose.yml", HostID: hostID,
	})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	srv := &Server{cfg: config.Config{DataDir: dir}, store: st, docker: docker.NewManager(st)}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	return srv, pid
}

// PENTEST: an MCP-driven deploy to a remote host must refuse a bind mount that
// points outside the project folder, exactly as the web UI does, unless the
// project was explicitly opted in.
func TestMCPRemoteDeployRefusesExternalBinds(t *testing.T) {
	ctx := context.Background()
	if !docker.ComposeAvailable(ctx) {
		t.Skip("the `docker compose` CLI is required to resolve the project config")
	}
	// /etc on the REMOTE host — the whole point of the opt-in.
	srv, pid := remoteProjectServer(t, `
services:
  web:
    image: nginx:alpine
    volumes:
      - /etc:/host-etc:ro
`)

	_, err := srv.mcpDeployProject(ctx, pid, nil, false)
	if err == nil {
		t.Fatal("MCP deployed a remote project mounting /etc from the target host: the host-path opt-in was bypassed")
	}
	// Match on the substance of the refusal, not merely that something failed —
	// a compose syntax error or an unreachable daemon would also produce an error
	// here and would make this test pass for the wrong reason.
	if !strings.Contains(err.Error(), "outside the project folder") {
		t.Fatalf("refused, but not by the host-path guard: %v", err)
	}
}

// The mirror of the above: the guard must not be so broad that it blocks an
// ordinary remote deploy. A bind INSIDE the project folder is the normal case —
// those get shipped to the target host rather than refused — so it must get past
// the bind classification. It still fails afterwards (the test host 127.0.0.1:1
// does not exist), which is why this asserts on WHICH error came back.
func TestMCPRemoteDeployAllowsInternalBinds(t *testing.T) {
	ctx := context.Background()
	if !docker.ComposeAvailable(ctx) {
		t.Skip("the `docker compose` CLI is required to resolve the project config")
	}
	srv, pid := remoteProjectServer(t, `
services:
  web:
    image: nginx:alpine
    volumes:
      - ./site:/usr/share/nginx/html:ro
`)

	_, err := srv.mcpDeployProject(ctx, pid, nil, false)
	if err == nil {
		return // reached the daemon; the classification step is what mattered
	}
	if strings.Contains(err.Error(), "outside the project folder") {
		t.Fatalf("a bind inside the project folder was refused as an external host path: %v", err)
	}
	// Getting as far as shipping the files to the (nonexistent) host proves the
	// bind was classified internal and the deploy proceeded past the guard.
	if !strings.Contains(err.Error(), "copying the project files") {
		t.Fatalf("failed before the bind classification, so this proves nothing: %v", err)
	}
}
