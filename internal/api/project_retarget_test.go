package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Retargeting a deployed project used to leave the stack running on the host it
// moved away from: the app kept no record that anything was there, so the
// operator ended up with two live copies and only one of them visible as "the
// project's host". Bringing the old one down is now an explicit, opt-in part of
// the change.
//
// The teardown itself needs a real daemon, so these tests pin the parts that
// decide WHETHER and IN WHICH ORDER it happens — which is where a mistake would
// strand a stack somewhere the app no longer points at.

func retargetFixture(t *testing.T) (*Server, *store.Store, int64, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	srv := &Server{cfg: config.Config{DataDir: t.TempDir()}, store: st}

	if _, err := st.CreateHost(ctx, &store.Host{Name: "local", Kind: "local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateHost(ctx, &store.Host{Name: "prod", Kind: "tcp", Address: "127.0.0.1:2376"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	// A real admin row: requireHostAccess resolves the caller from the store, so
	// claims pointing at a nonexistent user are simply unauthorized.
	admin, err := st.CreateUser(ctx, &store.User{Username: "root", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return srv, st, id, admin
}

// patch drives the settings endpoint the way the UI does.
func patchRetarget(t *testing.T, srv *Server, id int64, body string, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/api/projects/"+strconv.FormatInt(id, 10), strings.NewReader(body)).
		WithContext(ctxAs(uid, "admin"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	srv.handleRenameProject(w, r)
	return w
}

// Not asking for the teardown must leave the old host alone — the behaviour that
// shipped before this existed, and the one an operator relies on when they are
// deliberately standing up a second copy.
func TestRetarget_WithoutTeardownJustMovesTheRecord(t *testing.T) {
	srv, st, id, admin := retargetFixture(t)
	ctx := context.Background()

	w := patchRetarget(t, srv, id, `{"name":"app","hostId":2,"allowRemoteHostPaths":false,"tearDownOldHost":false}`, admin)
	if w.Code != 200 {
		t.Fatalf("patch = %d (%s)", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if _, ok := got["tornDown"]; ok {
		t.Errorf("nothing should have been torn down: %v", got)
	}
	p, _ := st.ProjectByID(ctx, id)
	if p.HostID != 2 {
		t.Errorf("host = %d, want the new host 2", p.HostID)
	}
}

// The record must not move when the teardown was asked for and failed. Moving it
// anyway would leave a running stack on a host the app no longer associates with
// the project — precisely the state this feature exists to prevent, made worse by
// being invisible.
func TestRetarget_FailedTeardownDoesNotMoveTheProject(t *testing.T) {
	srv, st, id, admin := retargetFixture(t)
	ctx := context.Background()

	// Point the project at the unreachable TCP host first, then try to move it
	// back. The teardown must run against that host and fail.
	if err := st.UpdateProjectSettings(ctx, id, "app", 2, false); err != nil {
		t.Fatal(err)
	}
	w := patchRetarget(t, srv, id, `{"name":"app","hostId":0,"allowRemoteHostPaths":false,"tearDownOldHost":true}`, admin)
	if w.Code == 200 {
		t.Fatalf("a failed teardown must not report success: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "NOT moved") {
		t.Errorf("the error should say the project stayed put, got %s", w.Body.String())
	}
	p, _ := st.ProjectByID(ctx, id)
	if p.HostID != 2 {
		t.Errorf("SECURITY/CORRECTNESS: host moved to %d despite the teardown failing — the old stack is now orphaned and invisible", p.HostID)
	}
}

// Asking for a teardown while NOT changing the host is a no-op, not an
// accidental "down". A rename must never stop a running project.
func TestRetarget_SameHostNeverTearsDown(t *testing.T) {
	srv, st, id, admin := retargetFixture(t)
	ctx := context.Background()
	if err := st.UpdateProjectSettings(ctx, id, "app", 2, false); err != nil {
		t.Fatal(err)
	}

	// Same host id, teardown requested: the guard is `body.HostID != p.HostID`,
	// so this must succeed without touching Docker at all (the host is
	// unreachable, so any attempt would fail the request).
	w := patchRetarget(t, srv, id, `{"name":"renamed","hostId":2,"allowRemoteHostPaths":false,"tearDownOldHost":true}`, admin)
	if w.Code != 200 {
		t.Fatalf("a pure rename must not attempt a teardown: %d (%s)", w.Code, w.Body.String())
	}
	p, _ := st.ProjectByID(ctx, id)
	if p.Name != "renamed" || p.HostID != 2 {
		t.Errorf("project = %+v, want renamed and still on host 2", p)
	}
}

// PENTEST: the teardown targets the host the project is LEAVING, and that host
// needs the caller's authority just as much as the one it is moving to.
// Otherwise "move it away" becomes a way to stop workloads on a host you were
// scoped away from.
func TestPentestRetarget_TeardownChecksTheOldHostsPermission(t *testing.T) {
	srv, st, id, _ := retargetFixture(t)
	ctx := context.Background()
	if err := st.UpdateProjectSettings(ctx, id, "app", 2, false); err != nil {
		t.Fatal(err)
	}

	// A user granted projects everywhere, but hosts nowhere: they cannot act on
	// the remote host, so they must not be able to bring it down either.
	uid, _ := st.CreateUser(ctx, &store.User{Username: "dev", Role: "user", Sections: []string{"projects"}})
	r := httptest.NewRequest("PATCH", "/api/projects/"+strconv.FormatInt(id, 10),
		strings.NewReader(`{"name":"app","hostId":0,"allowRemoteHostPaths":false,"tearDownOldHost":true}`)).
		WithContext(ctxAs(uid, "user"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	srv.handleRenameProject(w, r)

	// 403 specifically, not merely "not 200": without the check the request still
	// fails, but with a 502 from the unreachable daemon — which would let this
	// test pass while proving nothing. The status is what distinguishes "you may
	// not" from "it didn't work".
	if w.Code != 403 {
		t.Errorf("SECURITY: a user without the hosts permission got %d, want 403 — the permission check is not what stopped them: %s",
			w.Code, w.Body.String())
	}
	if p, _ := st.ProjectByID(ctx, id); p.HostID != 2 {
		t.Errorf("the project should not have moved either, got host %d", p.HostID)
	}
}
