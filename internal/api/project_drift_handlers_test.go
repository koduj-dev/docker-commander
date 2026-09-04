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
	return driftRequestWithValue(srv, pid, uid, role, action, service, kind, "", "", "")
}

func driftRequestWithValue(srv *Server, pid, uid int64, role, action, service, kind, from, to, detail string) *httptest.ResponseRecorder {
	sid := strconv.FormatInt(pid, 10)
	body, _ := json.Marshal(map[string]string{"service": service, "kind": kind, "from": from, "to": to, "detail": detail})
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

// The stored ignore must be scoped to the specific from/to values submitted,
// not just (service, kind) — otherwise ignoring one env change would also
// silently accept every future, unrelated env change on that service.
func TestHandleIgnoreDrift_ScopesToFingerprintOfSpecificChange(t *testing.T) {
	srv, pid, admin := newDriftTestServer(t)

	w := driftRequestWithValue(srv, pid, admin, "admin", "ignore", "web", "env", "FOO=1", "FOO=2", "")
	if w.Code != 200 {
		t.Fatalf("ignore status = %d: %s", w.Code, w.Body.String())
	}
	list, err := srv.store.ListDriftIgnores(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %+v, want one entry", list)
	}
	want := docker.ChangeFingerprint(docker.ServiceChange{Kind: "env", From: "FOO=1", To: "FOO=2"})
	if list[0].Fingerprint != want {
		t.Errorf("Fingerprint = %q, want %q (derived from the submitted from/to)", list[0].Fingerprint, want)
	}
	other := docker.ChangeFingerprint(docker.ServiceChange{Kind: "env", From: "BAR=1", To: "BAR=2"})
	if list[0].Fingerprint == other {
		t.Errorf("a different env change must not fingerprint the same as FOO=1->FOO=2")
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
	//
	// The ignore request carries the change's actual from/to/detail, exactly
	// like the real frontend does (it ignores from the preview it already has
	// on screen) — the server fingerprints those, so an ignore call that
	// omitted them would (correctly) fail to match this specific change.
	first := previewRequest(srv, pid, admin, "admin")
	if first.Code != 200 {
		t.Fatalf("preview status = %d: %s", first.Code, first.Body.String())
	}
	var pre mcp.ProjectPreview
	if err := json.Unmarshal(first.Body.Bytes(), &pre); err != nil {
		t.Fatalf("decode: %v (%s)", err, first.Body.String())
	}
	if len(pre.Changes) != 1 {
		t.Fatalf("expected exactly one change before ignoring, got %+v", pre.Changes)
	}
	change := pre.Changes[0]

	if w := driftRequestWithValue(srv, pid, admin, "admin", "ignore", change.Service, change.Kind, change.From, change.To, change.Detail); w.Code != 200 {
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
