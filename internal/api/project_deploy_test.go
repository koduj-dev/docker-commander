package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// The whole point of persisting "last deployed profiles" is telling a service
// EXCLUDED by the profiles actually used at the last deploy apart from one
// that's simply stopped — the compose-profiles UX gap these tests exist for
// (see NEXT.md / CHANGELOG). They drive a REAL local `docker compose up` so
// the persisted value is what actually happened, not a guess.

const deployTestImage = "alpine:latest"

// freeDeployStack force-removes any containers left over from a previous
// (possibly killed) run of this test, by compose project label. A fixed slug
// across runs means a run that never reached t.Cleanup would otherwise
// collide with "name already in use" on the next one.
func freeDeployStack(slug string) {
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+slug).Output()
	if err != nil {
		return
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return
	}
	args := append([]string{"rm", "-f"}, ids...)
	_ = exec.Command("docker", args...).Run()
}

// deployTestServer sets up a store + a project folder on disk with the given
// compose content, targeting the local daemon (host 0). Host 0 needs no
// "hosts" permission, but every {id} route still resolves the project through
// loadProject, which checks the "projects" section against an authenticated
// user — so this also creates an admin to drive the requests as.
func deployTestServer(t *testing.T, slug, compose string) (*Server, *store.Store, int64, int64) {
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
	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: slug, ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	return srv, st, pid, admin
}

func deployRequest(srv *Server, pid, uid int64, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/deploy", strings.NewReader(body)).
		WithContext(ctxAs(uid, "admin"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(pid, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	srv.handleDeployProject(w, r)
	return w
}

func getProjectJSON(t *testing.T, srv *Server, pid, uid int64) projectJSON {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/projects/"+strconv.FormatInt(pid, 10), nil).
		WithContext(ctxAs(uid, "admin"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(pid, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	srv.handleGetProject(w, r)
	var pj projectJSON
	if err := json.Unmarshal(w.Body.Bytes(), &pj); err != nil {
		t.Fatalf("could not decode project JSON: %v (%s)", err, w.Body.String())
	}
	return pj
}

func TestHandleDeployProject_PersistsLastDeployedProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	const slug = "dctest-deploy-profiles"
	compose := `
services:
  web:
    image: ` + deployTestImage + `
    command: ["sleep", "300"]
  worker:
    image: ` + deployTestImage + `
    command: ["sleep", "300"]
    profiles: ["extra"]
`
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	// Before any deploy: no profiles recorded — not an error, not a placeholder.
	if p, err := st.ProjectByID(context.Background(), pid); err != nil || len(p.LastDeployedProfiles) != 0 {
		t.Fatalf("expected no profiles before deploy, got %v (err=%v)", p, err)
	}

	// Deploy WITH the "extra" profile, no rebuild needed for a plain image.
	w := deployRequest(srv, pid, admin, `{"profiles":["extra"],"build":false}`)
	if w.Code != 200 {
		t.Fatalf("deploy request failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("deploy did not succeed: %s / %s", resp.Error, resp.Output)
	}

	p, err := st.ProjectByID(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.LastDeployedProfiles) != 1 || p.LastDeployedProfiles[0] != "extra" {
		t.Fatalf("last deployed profiles = %v, want [extra]", p.LastDeployedProfiles)
	}

	// And it's exposed to the frontend via the project JSON, wherever the
	// project's state is already returned.
	pj := getProjectJSON(t, srv, pid, admin)
	if len(pj.LastDeployedProfiles) != 1 || pj.LastDeployedProfiles[0] != "extra" {
		t.Errorf("projectJSON did not expose the deployed profiles: %v", pj.LastDeployedProfiles)
	}
}

// A failed deploy must not advance "last deployed profiles" past a profile
// set that isn't actually running — otherwise a badge could claim a service is
// merely "not in the active profile" when the deploy that would have started
// it never actually succeeded.
func TestHandleDeployProject_FailedDeployDoesNotUpdateProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	const slug = "dctest-deploy-profiles-fail"
	// Deliberately invalid: a service needs `image` or `build`.
	compose := "services:\n  web:\n    command: [\"true\"]\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() { freeDeployStack(slug) })

	w := deployRequest(srv, pid, admin, `{"profiles":["extra"],"build":false}`)
	if w.Code != 200 {
		t.Fatalf("a failed deploy reports failure in the body, not the status: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("expected the deploy to fail (no image/build on the service)")
	}

	p, err := st.ProjectByID(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.LastDeployedProfiles) != 0 {
		t.Errorf("a failed deploy must not persist profiles, got %v", p.LastDeployedProfiles)
	}
}
