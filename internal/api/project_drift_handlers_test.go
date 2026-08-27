package api

import (
	"bytes"
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

func driftRequest(srv *Server, pid, uid int64, role, action, service, kind string) *httptest.ResponseRecorder {
	sid := strconv.FormatInt(pid, 10)
	body, _ := json.Marshal(map[string]string{"service": service, "kind": kind})
	r := httptest.NewRequest("POST", "/api/projects/"+sid+"/drift/"+action, bytes.NewReader(body)).WithContext(ctxAs(uid, role))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sid)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	if action == "ignore" {
		srv.handleIgnoreDrift(w, r)
	} else {
		srv.handleUnignoreDrift(w, r)
	}
	return w
}

func newDriftTestServer(t *testing.T) (*Server, int64, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	dir := t.TempDir()
	srv := &Server{cfg: config.Config{DataDir: dir}, store: st, docker: docker.NewManager(st)}

	admin, err := st.CreateUser(ctx, &store.User{Username: "root", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "dctest-drift", ComposeFile: "compose.yml"})
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
	return srv, pid, admin
}

func TestHandleIgnoreDrift_PersistsAndUnignore(t *testing.T) {
	srv, pid, admin := newDriftTestServer(t)

	w := driftRequest(srv, pid, admin, "admin", "ignore", "web", "env")
	if w.Code != 200 {
		t.Fatalf("ignore status = %d: %s", w.Code, w.Body.String())
	}
	list, err := srv.store.ListDriftIgnores(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Service != "web" || list[0].Kind != "env" {
		t.Fatalf("list = %+v, want one web:env entry", list)
	}

	w = driftRequest(srv, pid, admin, "admin", "unignore", "web", "env")
	if w.Code != 200 {
		t.Fatalf("unignore status = %d: %s", w.Code, w.Body.String())
	}
	list, err = srv.store.ListDriftIgnores(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("list = %+v, want empty after unignore", list)
	}
}

func TestHandleIgnoreDrift_RequiresServiceAndKind(t *testing.T) {
	srv, pid, admin := newDriftTestServer(t)
	w := driftRequest(srv, pid, admin, "admin", "ignore", "", "env")
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for a missing service", w.Code)
	}
}

// PENTEST: ignoring/unignoring drift mutates project state (a POST), so a
// read-only "projects" grant must be denied — same rule as deploy/validate.
func TestHandleIgnoreDrift_ReadOnlyUserDenied(t *testing.T) {
	srv, pid, _ := newDriftTestServer(t)
	viewer, err := srv.store.CreateUser(context.Background(), &store.User{
		Username: "viewer", Role: "user", Sections: []string{"projects"}, ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := driftRequest(srv, pid, viewer, "user", "ignore", "web", "env")
	if w.Code != 403 {
		t.Errorf("status = %d, want 403 for a read-only grant", w.Code)
	}
}

// End-to-end: an ignored drift still appears in the preview (auditable,
// reversible) but no longer counts toward Active.
func TestHandlePreviewProject_IgnoredDriftExcludedFromActive(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, pid, admin := newDriftTestServer(t)

	// "added" is a real, deterministic change for this never-deployed project
	// (no live daemon involved), so the mechanism can be tested without a
	// running container.
	if w := driftRequest(srv, pid, admin, "admin", "ignore", "web", "added"); w.Code != 200 {
		t.Fatalf("ignore status = %d: %s", w.Code, w.Body.String())
	}

	w := previewRequest(srv, pid, admin, "admin")
	if w.Code != 200 {
		t.Fatalf("preview status = %d: %s", w.Code, w.Body.String())
	}
	var prev mcp.ProjectPreview
	if err := json.Unmarshal(w.Body.Bytes(), &prev); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(prev.Changes) != 1 || !prev.Changes[0].Ignored {
		t.Fatalf("expected one change marked ignored, got %+v", prev.Changes)
	}
	if prev.Active != 0 {
		t.Errorf("Active = %d, want 0 (the only change is ignored)", prev.Active)
	}
}
