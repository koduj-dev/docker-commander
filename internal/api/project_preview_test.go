package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/mcp"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func previewRequest(srv *Server, pid, uid int64, role string) *httptest.ResponseRecorder {
	sid := strconv.FormatInt(pid, 10)
	r := httptest.NewRequest("GET", "/api/projects/"+sid+"/preview", nil).WithContext(ctxAs(uid, role))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sid)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	srv.handlePreviewProject(w, r)
	return w
}

// The web UI's preview screen must see exactly what the `preview_deploy` MCP
// tool already reports (see mcpPreviewProject) — this is that same
// comparison exposed as a first-class GET route instead of an MCP-only one.
func TestHandlePreviewProject_ReportsAddedService(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dir := t.TempDir()
	srv := &Server{cfg: config.Config{DataDir: dir}, store: st, docker: docker.NewManager(st)}

	admin, err := st.CreateUser(ctx, &store.User{Username: "root", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "dctest-preview-added", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	w := previewRequest(srv, pid, admin, "admin")
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var prev mcp.ProjectPreview
	if err := json.Unmarshal(w.Body.Bytes(), &prev); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if !prev.Valid {
		t.Fatalf("expected a valid preview, got error: %s", prev.Error)
	}
	if len(prev.Changes) != 1 || prev.Changes[0].Kind != "added" || prev.Changes[0].Service != "web" {
		t.Errorf("expected one 'added' change for service web, got %+v", prev.Changes)
	}
}

// PENTEST/RBAC: preview is read-only (a GET, no mutation), so a user whose
// grant on "projects" is read-only must still be able to call it — unlike
// deploy/validate/resolve, which are POSTs and require write. If this ever
// regresses to needing write, a read-only operator loses the one screen that
// would let them safely review a deploy before asking someone else to run it.
func TestHandlePreviewProject_ReadOnlyUserAllowed(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dir := t.TempDir()
	srv := &Server{cfg: config.Config{DataDir: dir}, store: st, docker: docker.NewManager(st)}

	viewer, err := st.CreateUser(ctx, &store.User{
		Username: "viewer", Role: "user", Sections: []string{"projects"}, ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "dctest-preview-ro", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	w := previewRequest(srv, pid, viewer, "user")
	if w.Code != 200 {
		t.Fatalf("a read-only projects grant should still see the preview, got status %d: %s", w.Code, w.Body.String())
	}
}

// A user with no "projects" grant at all must not be able to distinguish a
// project they can't see from one that doesn't exist.
func TestHandlePreviewProject_NoGrantNotFound(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dir := t.TempDir()
	srv := &Server{cfg: config.Config{DataDir: dir}, store: st, docker: docker.NewManager(st)}

	outsider, err := st.CreateUser(ctx, &store.User{Username: "outsider", Role: "user", Sections: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "dctest-preview-deny", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}

	w := previewRequest(srv, pid, outsider, "user")
	if w.Code != 404 {
		t.Errorf("expected 404 for a user with no grant on this project, got %d: %s", w.Code, w.Body.String())
	}
}
