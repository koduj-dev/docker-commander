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

// runningServiceCount reports how many containers are currently running for a
// given compose project + service, by label — so a test can assert what
// actually got deployed, not just what got persisted.
func runningServiceCount(t *testing.T, slug, service string) int {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+slug,
		"--filter", "label=com.docker.compose.service="+service,
	).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	return len(strings.Fields(string(out)))
}

// deployTestServer sets up a store + a project folder on disk with the given
// compose content, targeting the local daemon (host 0). Host 0 needs no
// "hosts" permission, but every {id} route still resolves the project through
// loadProject, which checks the "projects" section against an authenticated
// user — so this also creates an admin to drive the requests as.
func deployTestServer(t *testing.T, slug, compose string) (*Server, *store.Store, int64, int64) {
	t.Helper()
	return deployTestServerAt(t, ":memory:", slug, compose)
}

// deployTestServerAt is deployTestServer with an explicit sqlite path instead
// of an in-memory DB — needed by anything that must open a second raw
// connection to the same database file (an in-memory DB is per-connection and
// shares nothing across separate *sql.DB handles).
func deployTestServerAt(t *testing.T, dbPath, slug, compose string) (*Server, *store.Store, int64, int64) {
	t.Helper()
	st, err := store.Open(dbPath)
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

// TestHandleDeployProject_NormalizesProfilesBeforePersistAndAudit is the
// DC-COR-005 regression: a request selecting the same profile three
// different ways (padded with whitespace, exact duplicate, and an empty
// entry) must persist and audit the SAME normalized value the compose
// command actually acted on ("extra", once) — not the raw three-element
// slice, which would make the "Deployed with" badge and the audit log
// disagree with what docker compose was actually told to run.
func TestHandleDeployProject_NormalizesProfilesBeforePersistAndAudit(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	const slug = "dctest-deploy-profiles-norm"
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

	w := deployRequest(srv, pid, admin, `{"profiles":[" extra ","extra",""],"build":false}`)
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
		t.Fatalf("last deployed profiles = %v, want the normalized [extra], not the raw 3-element request", p.LastDeployedProfiles)
	}

	entries, err := st.RecentAudit(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "project.deploy" && e.Target == slug {
			found = true
			if e.Detail != "extra" {
				t.Errorf("audit detail = %q, want the normalized %q, not the raw request", e.Detail, "extra")
			}
			break
		}
	}
	if !found {
		t.Fatal("no project.deploy audit entry found")
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

// A project's own .env file (auto-loaded by the compose CLI from its working
// directory) or the server process's own environment can set COMPOSE_PROFILES,
// which — left alone — activates MORE than the profiles actually selected in
// the deploy request. That would badge a genuinely-running service as "not in
// active profile" (see docs/gotchas.md), the exact bug this whole feature
// exists to prevent, reintroduced by a side channel. ComposeUpFiles
// neutralizes COMPOSE_PROFILES for the subprocess; this proves it against a
// REAL `docker compose up`, not just that the persisted list matches the
// request — persisting exactly body.Profiles would look identical even if the
// neutralization did nothing, since the server always persists what was
// *requested*, never what Compose actually activated.
func TestHandleDeployProject_EnvFileComposeProfilesDoesNotLeakIn(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	const slug = "dctest-deploy-profiles-envfile"
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
	// The project's own .env, auto-loaded by `docker compose` from its working
	// directory, activates "extra" — which is NOT in the deploy request below.
	if err := os.WriteFile(filepath.Join(srv.projectRoot(pid), ".env"), []byte("COMPOSE_PROFILES=extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	// Deploy with NO profiles selected — the reproducing case: Compose falls
	// back to COMPOSE_PROFILES (env/.env) only when NO --profile flag is on the
	// command line at all. A non-empty selection wouldn't reproduce the leak —
	// see docs/gotchas.md: once any --profile flag is given, Compose uses it
	// exclusively and ignores COMPOSE_PROFILES entirely.
	w := deployRequest(srv, pid, admin, `{"profiles":[],"build":false}`)
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
	if len(p.LastDeployedProfiles) != 0 {
		t.Errorf("last deployed profiles = %v, want none (request selected none)", p.LastDeployedProfiles)
	}

	// The real proof: "worker" (profile "extra", from the .env file, not the
	// request) must NOT actually be running. If COMPOSE_PROFILES leaked
	// through, this is where it would show up — the persisted list matching
	// the request is necessary but not sufficient.
	if n := runningServiceCount(t, slug, "worker"); n != 0 {
		t.Errorf("worker should not be running (only .env's COMPOSE_PROFILES=extra activated it, not the deploy request), but %d container(s) running", n)
	}
	if n := runningServiceCount(t, slug, "web"); n != 1 {
		t.Errorf("web should be running, got %d container(s)", n)
	}
}

// TestHandleDeployProject_PullForcesARegistryCheck is the HTTP-layer half of
// internal/docker's TestIntegrationComposeUpFilesPull_AlwaysChecksTheRegistry:
// {"pull": true} in the deploy request body must actually reach
// ComposeUpFilesPull, not just be accepted and ignored. Same real-output
// assertion ("Pulling" only appears when Compose was told to check), and the
// negative case (a plain deploy) is asserted too — a test that only checked
// "pull:true 'Pulling' appears" would still pass if Compose showed that line
// on every deploy regardless of the flag.
func TestHandleDeployProject_PullForcesARegistryCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-deploy-pull"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n"
	srv, _, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	deployOutput := func(body string) string {
		t.Helper()
		w := deployRequest(srv, pid, admin, body)
		if w.Code != 200 {
			t.Fatalf("deploy status = %d: %s", w.Code, w.Body.String())
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
		return resp.Output
	}

	if out := deployOutput(`{"build":false}`); strings.Contains(out, "Pulling") {
		t.Errorf("a plain deploy (pull absent/false) must not check the registry, got:\n%s", out)
	}
	if out := deployOutput(`{"build":false,"pull":true}`); !strings.Contains(out, "Pulling") {
		t.Errorf("pull:true must force a registry check even though the image is already local, got:\n%s", out)
	}
}
