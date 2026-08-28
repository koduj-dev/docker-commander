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

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// revisionRouteCtx builds a chi route context carrying {id} and, when rev > 0,
// {rev} — the two path params every revision route needs.
func revisionRouteCtx(pid int64, rev int) *chi.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(pid, 10))
	if rev > 0 {
		rctx.URLParams.Add("rev", strconv.Itoa(rev))
	}
	return rctx
}

func listRevisionsRequest(srv *Server, pid, uid int64, role string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/projects/"+strconv.FormatInt(pid, 10)+"/revisions", nil).WithContext(ctxAs(uid, role))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, revisionRouteCtx(pid, 0)))
	w := httptest.NewRecorder()
	srv.handleListRevisions(w, r)
	return w
}

func getRevisionRequest(srv *Server, pid int64, rev int, uid int64, role string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/projects/x/revisions/x", nil).WithContext(ctxAs(uid, role))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, revisionRouteCtx(pid, rev)))
	w := httptest.NewRecorder()
	srv.handleGetRevision(w, r)
	return w
}

func diffRevisionRequest(srv *Server, pid int64, rev int, against string, uid int64, role string) *httptest.ResponseRecorder {
	url := "/api/projects/x/revisions/x/diff"
	if against != "" {
		url += "?against=" + against
	}
	r := httptest.NewRequest("GET", url, nil).WithContext(ctxAs(uid, role))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, revisionRouteCtx(pid, rev)))
	w := httptest.NewRecorder()
	srv.handleRevisionDiff(w, r)
	return w
}

func restoreRevisionRequest(srv *Server, pid int64, rev int, uid int64, role, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/projects/x/revisions/x/restore", strings.NewReader(body)).WithContext(ctxAs(uid, role))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, revisionRouteCtx(pid, rev)))
	w := httptest.NewRecorder()
	srv.handleRestoreRevision(w, r)
	return w
}

func mustDeploy(t *testing.T, srv *Server, pid, admin int64, body string) {
	t.Helper()
	w := deployRequest(srv, pid, admin, body)
	if w.Code != 200 {
		t.Fatalf("deploy request failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("deploy did not succeed: %s / %s", resp.Error, resp.Output)
	}
}

// A successful deploy must record a revision: its author, the profiles it
// ran with, and (best-effort) the service's image reference.
func TestCaptureRevision_OnSuccessfulDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-revision-capture"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	// captureRevisionImages resolves the local Docker API client via host id
	// 0 — deployTestServer never registers a "local" host row (ComposeUpFiles
	// itself only shells out to the compose CLI, so it doesn't need one), so
	// without this the image-capture half silently finds nothing to resolve.
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	mustDeploy(t, srv, pid, admin, `{"build":false}`)

	list, err := st.ListRevisions(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d revisions, want 1: %+v", len(list), list)
	}
	rev := list[0]
	// Author is "" here: ctxAs (the shared test-request helper) never sets
	// Claims.Username, only UserID/Role — a real session always carries it.
	// TestCaptureRevision_RecordsTheAuthenticatedAuthor below drives a
	// request with Username set to cover that half specifically.
	if rev.Revision != 1 || !rev.Valid {
		t.Errorf("revision = %+v", rev)
	}
	if len(rev.Images) != 1 || rev.Images[0].Service != "web" || rev.Images[0].Image != deployTestImage {
		t.Errorf("Images = %+v", rev.Images)
	}
}

// A deploy driven by a request whose claims actually carry a username (as a
// real session's do) must record it as the revision's author.
func TestCaptureRevision_RecordsTheAuthenticatedAuthor(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-revision-author"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	r := httptest.NewRequest("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/deploy", strings.NewReader(`{"build":false}`)).
		WithContext(auth.WithClaims(context.Background(), &auth.Claims{UserID: admin, Username: "root", Role: "admin"}))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, revisionRouteCtx(pid, 0)))
	w := httptest.NewRecorder()
	srv.handleDeployProject(w, r)
	if w.Code != 200 {
		t.Fatalf("deploy status = %d: %s", w.Code, w.Body.String())
	}

	rev, err := st.LatestRevision(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Author != "root" {
		t.Errorf("Author = %q, want %q", rev.Author, "root")
	}
}

// The centerpiece: deploy two different revisions, restore to the first, and
// verify the live compose file, the running env, and the profiles all go
// back — AND that the restore itself becomes a new (third) revision rather
// than rewriting history.
func TestRestoreRevision_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-revision-restore"
	composeV1 := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    environment:\n      FOO: v1\n"
	srv, st, pid, admin := deployTestServer(t, slug, composeV1)
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	mustDeploy(t, srv, pid, admin, `{"build":false}`)

	// Edit to v2 and deploy again.
	composeV2 := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    environment:\n      FOO: v2\n"
	root := srv.projectRoot(pid)
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(composeV2), 0o644); err != nil {
		t.Fatal(err)
	}
	mustDeploy(t, srv, pid, admin, `{"build":false}`)

	list, err := st.ListRevisions(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d revisions before restore, want 2: %+v", len(list), list)
	}

	// Restore to revision 1 (the v1 content).
	w := restoreRevisionRequest(srv, pid, 1, admin, "admin", `{}`)
	if w.Code != 200 {
		t.Fatalf("restore status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("restore did not succeed: %s / %s", resp.Error, resp.Output)
	}

	// The live file must match v1 again.
	got, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != composeV1 {
		t.Errorf("compose.yml after restore = %q, want the v1 content %q", got, composeV1)
	}

	// History grows forward: a third revision records the restore, not a
	// rewrite of revision 1 or 2.
	list, err = st.ListRevisions(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d revisions after restore, want 3: %+v", len(list), list)
	}
	if list[0].Revision != 3 || !strings.Contains(list[0].Reason, "revision 1") {
		t.Errorf("newest revision = %+v, want revision 3 explaining the restore", list[0])
	}
}

// Restoring an unknown revision number is a 404, not a 500 or a silent no-op.
func TestRestoreRevision_UnknownRevisionIs404(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	srv, _, pid, admin := deployTestServer(t, "dctest-revision-404", compose)
	w := restoreRevisionRequest(srv, pid, 99, admin, "admin", `{}`)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404 for an unknown revision", w.Code)
	}
}

// PENTEST/RBAC: restoring mutates the project (redeploys it), so a read-only
// "projects" grant must be denied — same rule as deploy itself.
func TestRestoreRevision_ReadOnlyUserDenied(t *testing.T) {
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	srv, st, pid, _ := deployTestServer(t, "dctest-revision-ro", compose)
	viewer, err := st.CreateUser(context.Background(), &store.User{
		Username: "viewer", Role: "user", Sections: []string{"projects"}, ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := restoreRevisionRequest(srv, pid, 1, viewer, "user", `{}`)
	if w.Code != 403 {
		t.Errorf("status = %d, want 403 for a read-only grant", w.Code)
	}
}

func TestListAndGetRevision(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	srv, _, pid, admin := deployTestServer(t, "dctest-revision-list", compose)
	freeDeployStack("dctest-revision-list")
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), "dctest-revision-list", nil)
		freeDeployStack("dctest-revision-list")
	})
	mustDeploy(t, srv, pid, admin, `{"build":false,"reason":"initial rollout"}`)

	w := listRevisionsRequest(srv, pid, admin, "admin")
	if w.Code != 200 {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var list []struct {
		Revision int    `json:"revision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Reason != "initial rollout" {
		t.Fatalf("list = %+v", list)
	}

	w = getRevisionRequest(srv, pid, 1, admin, "admin")
	if w.Code != 200 {
		t.Fatalf("get status = %d: %s", w.Code, w.Body.String())
	}
	w = getRevisionRequest(srv, pid, 99, admin, "admin")
	if w.Code != 404 {
		t.Errorf("get unknown revision status = %d, want 404", w.Code)
	}
}

// The revision diff endpoint must reuse the exact same comparison the
// deploy preview uses (env change, recreates flag) — proving revisions and
// live-preview share one engine rather than two subtly different ones.
func TestRevisionDiff_AgainstCurrentAndAgainstAnotherRevision(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-revision-diff"
	composeV1 := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    environment:\n      FOO: v1\n"
	srv, st, pid, admin := deployTestServer(t, slug, composeV1)
	// "against=current" resolves the live containers via the Docker API
	// (host id 0), which needs a registered local host — see the comment in
	// TestCaptureRevision_OnSuccessfulDeploy.
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})
	mustDeploy(t, srv, pid, admin, `{"build":false}`)

	composeV2 := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    environment:\n      FOO: v2\n"
	root := srv.projectRoot(pid)
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(composeV2), 0o644); err != nil {
		t.Fatal(err)
	}
	mustDeploy(t, srv, pid, admin, `{"build":false}`)

	// Revision 1 vs what's running now (v2) must show the env change.
	w := diffRevisionRequest(srv, pid, 1, "current", admin, "admin")
	if w.Code != 200 {
		t.Fatalf("diff status = %d: %s", w.Code, w.Body.String())
	}
	var diff struct {
		Valid   bool `json:"valid"`
		Changes []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if !diff.Valid {
		t.Fatal("expected a valid diff")
	}
	foundEnv := false
	for _, c := range diff.Changes {
		if c.Kind == "env" {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Errorf("expected an env change between revision 1 and the currently-running v2, got %+v", diff.Changes)
	}

	// Revision 1 vs revision 2 directly (not "current") must show the same shape.
	w = diffRevisionRequest(srv, pid, 1, "2", admin, "admin")
	if w.Code != 200 {
		t.Fatalf("diff(1,2) status = %d: %s", w.Code, w.Body.String())
	}
	diff.Changes = nil
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	foundEnv = false
	for _, c := range diff.Changes {
		if c.Kind == "env" {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Errorf("expected an env change between revision 1 and revision 2, got %+v", diff.Changes)
	}
}

// TestRevisionDiff_UnchangedVolumesDoNotFalsePositive is the exact bug a
// real user hit: a project with a relative bind mount and a named volume,
// deployed once and never touched again, showed a spurious "volumes"
// change when diffed against itself. Two independent causes, both fixed
// here — a named volume's compose-declared name ("webdata") never matched
// the live container's actual project-prefixed one ("<slug>_webdata"), and
// a relative bind mount resolves to an absolute path anchored to whatever
// throwaway directory a revision happens to be extracted into, which is
// different on every call. Nothing about either mount changed, so the diff
// must report nothing.
func TestRevisionDiff_UnchangedVolumesDoNotFalsePositive(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-revision-volumes"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n" +
		"    volumes:\n      - webdata:/data\n      - ./html:/usr/share/nginx/html\n" +
		"volumes:\n  webdata: {}\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srv.projectRoot(pid), "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	freeDeployStack(slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = docker.ComposeDown(bg, srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
		_ = exec.Command("docker", "volume", "rm", "-f", slug+"_webdata").Run()
	})

	mustDeploy(t, srv, pid, admin, `{"build":false}`)

	w := diffRevisionRequest(srv, pid, 1, "current", admin, "admin")
	if w.Code != 200 {
		t.Fatalf("diff status = %d: %s", w.Code, w.Body.String())
	}
	var diff struct {
		Valid   bool `json:"valid"`
		Changes []struct {
			Service string `json:"service"`
			Kind    string `json:"kind"`
			From    string `json:"from"`
			To      string `json:"to"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if !diff.Valid {
		t.Fatal("expected a valid diff")
	}
	if len(diff.Changes) != 0 {
		t.Errorf("nothing actually changed since the deploy; expected no changes, got %+v", diff.Changes)
	}
}
