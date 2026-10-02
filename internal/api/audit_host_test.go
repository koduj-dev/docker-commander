package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// auditHostFixture: a local host plus two remote ones (A, B).
func auditHostFixture(t *testing.T) (srv *Server, st *store.Store, local, hostA, hostB int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	if local, err = st.LocalHostID(ctx); err != nil {
		t.Fatal(err)
	}
	if hostA, err = st.CreateHost(ctx, &store.Host{Name: "a", Kind: "tcp", Address: "tcp://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if hostB, err = st.CreateHost(ctx, &store.Host{Name: "b", Kind: "tcp", Address: "tcp://127.0.0.1:2"}); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: config.Config{}, store: st, docker: docker.NewManager(st)}, st, local, hostA, hostB
}

// serveAudited runs fn as a handler behind withAuditHost, the way the router does.
func serveAudited(t *testing.T, target string, fn func(w http.ResponseWriter, r *http.Request)) {
	t.Helper()
	r := httptest.NewRequest("POST", target, nil)
	r = r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{UserID: 1, Username: "admin", Role: "admin"}))
	withAuditHost(http.HandlerFunc(fn)).ServeHTTP(httptest.NewRecorder(), r)
}

func lastAuditHost(t *testing.T, st *store.Store) int64 {
	t.Helper()
	entries, err := st.RecentAudit(context.Background(), 1, 0, nil, true)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no audit entry: %v", err)
	}
	return entries[0].HostID
}

// An action on the local daemon with no ?host= (the UI's default) is recorded
// under the local host's own id, not 0. Before, it read as "no host".
func TestAuditNamesTheLocalDaemonWhenNoHostIsGiven(t *testing.T) {
	srv, st, local, _, _ := auditHostFixture(t)
	serveAudited(t, "/api/containers/abc/stop", func(w http.ResponseWriter, r *http.Request) {
		if _, err := srv.docker.Client(r.Context(), 0); err != nil {
			t.Fatal(err)
		}
		srv.audit(r, "container.stop", "abc", "")
	})
	if got := lastAuditHost(t, st); got != local {
		t.Fatalf("recorded host %d, want the local host %d", got, local)
	}
}

// An explicit ?host= is recorded as given.
func TestAuditNamesAnExplicitHost(t *testing.T) {
	srv, st, _, hostA, _ := auditHostFixture(t)
	serveAudited(t, "/api/containers/abc/stop?host=", func(w http.ResponseWriter, r *http.Request) {
		if _, err := srv.docker.Client(r.Context(), hostA); err != nil {
			t.Fatal(err)
		}
		srv.audit(r, "container.stop", "abc", "")
	})
	if got := lastAuditHost(t, st); got != hostA {
		t.Fatalf("recorded host %d, want %d", got, hostA)
	}
}

// An action that never reaches a daemon has no host: 0.
func TestAuditLeavesHostlessActionsAtZero(t *testing.T) {
	srv, st, _, _, _ := auditHostFixture(t)
	serveAudited(t, "/api/settings", func(w http.ResponseWriter, r *http.Request) {
		srv.audit(r, "settings.update", "", "")
	})
	if got := lastAuditHost(t, st); got != 0 {
		t.Fatalf("a hostless action was recorded under host %d", got)
	}
}

// A project action is recorded under the project's host: its remote host, or
// the local host's own id for a local project.
func TestAuditProjectActionsNameTheProjectsHost(t *testing.T) {
	srv, st, local, _, hostB := auditHostFixture(t)
	for _, tc := range []struct {
		hostID, want int64
	}{{hostB, hostB}, {0, local}} {
		p := &store.Project{ID: 1, Slug: "app", HostID: tc.hostID}
		serveAudited(t, "/api/projects/1/deploy", func(w http.ResponseWriter, r *http.Request) {
			srv.auditProject(r, p, "project.deploy", "")
		})
		if got := lastAuditHost(t, st); got != tc.want {
			t.Errorf("project on host %d recorded under %d, want %d", tc.hostID, got, tc.want)
		}
	}
}

// PENTEST: a reader limited to host A must not see what happened on host B.
// Project actions used to be recorded as 0, "no host", which every reader of the
// audit log sees, so deploys on B leaked to a reader limited to A.
func TestPentestAuditReaderLimitedToAHostDoesNotSeeAnotherHostsProject(t *testing.T) {
	srv, st, local, hostA, hostB := auditHostFixture(t)
	ctx := context.Background()

	write := func(p *store.Project, action string) {
		serveAudited(t, "/api/projects/x", func(w http.ResponseWriter, r *http.Request) {
			srv.auditProject(r, p, action, "")
		})
	}
	write(&store.Project{Slug: "on-a", HostID: hostA}, "project.deploy")
	write(&store.Project{Slug: "on-b", HostID: hostB}, "project.deploy")
	write(&store.Project{Slug: "on-local", HostID: 0}, "project.deploy")
	serveAudited(t, "/api/settings", func(w http.ResponseWriter, r *http.Request) {
		srv.audit(r, "settings.update", "global", "")
	})

	uid, err := st.CreateUser(ctx, &store.User{Username: "reader", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	roleID, err := st.CreateRole(ctx, &store.Role{
		Name: "Audit on A", Sections: []store.RoleSection{{Section: "audit"}}, HostIDs: []int64{hostA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRoles(ctx, uid, []int64{roleID}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/audit?limit=100", nil)
	r = r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{UserID: uid, Username: "reader", Role: "user"}))
	w := httptest.NewRecorder()
	srv.handleAudit(w, r)
	var got []store.AuditEntry
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.Target] = true
	}
	if seen["on-b"] {
		t.Error("SECURITY: a reader limited to host A sees a deploy on host B")
	}
	for _, want := range []string{"on-a", "on-local", "global"} {
		if !seen[want] {
			t.Errorf("a reader limited to host A should see %q (local and hostless entries are visible to every reader)", want)
		}
	}
	_ = local
}

// The router installs withAuditHost on every /api route. The tests above call
// it directly, so without this nothing would notice it being dropped from the
// router, and every action would silently go back to being recorded as 0.
func TestRouterInstallsTheAuditHostRecorder(t *testing.T) {
	srv, _, _, _, _ := auditHostFixture(t)
	root, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("Handler() is not a chi router")
	}
	want := reflect.ValueOf(withAuditHost).Pointer()
	for _, rt := range root.Routes() {
		if rt.Pattern != "/api/*" || rt.SubRoutes == nil {
			continue
		}
		for _, mw := range rt.SubRoutes.Middlewares() {
			if reflect.ValueOf(mw).Pointer() == want {
				return
			}
		}
		t.Fatal("the /api router doesn't use withAuditHost")
	}
	t.Fatal("no /api route found")
}

// A backup job's audit entries name the host it works on: its own for a
// volume job, its project's for a project job (the scheduler's rule).
func TestBackupJobHostFollowsTheSchedulersRule(t *testing.T) {
	srv, st, local, hostA, hostB := auditHostFixture(t)
	ctx := context.Background()
	cph, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(cph)
	volJob, err := st.CreateBackupJob(ctx, &store.BackupJob{Name: "v", Scope: store.BackupScopeVolume, VolumeName: "data", HostID: hostA, Image: "alpine", Command: "true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "p", Slug: "p", ComposeFile: "compose.yml", HostID: hostB})
	if err != nil {
		t.Fatal(err)
	}
	projJob, err := st.CreateBackupJob(ctx, &store.BackupJob{Name: "pj", Scope: store.BackupScopeProject, ProjectID: pid, HostID: hostA, Image: "alpine", Command: "true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	localJob, err := st.CreateBackupJob(ctx, &store.BackupJob{Name: "l", Scope: store.BackupScopeVolume, VolumeName: "data", Image: "alpine", Command: "true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, want int64
		what     string
	}{
		{volJob, hostA, "volume job"},
		{projJob, hostB, "project job (the project's host, not the job row's)"},
		{localJob, local, "local volume job (the local host's own id)"},
	} {
		if got := srv.backupJobHost(ctx, tc.id); got != tc.want {
			t.Errorf("%s: host %d, want %d", tc.what, got, tc.want)
		}
	}
}

// Actions on a host itself (/hosts/{id}/…) are recorded under that host.
func TestHostActionsNameTheHost(t *testing.T) {
	srv, st, _, hostA, _ := auditHostFixture(t)
	r := httptest.NewRequest("POST", "/api/hosts/x/trust", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(hostA, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	srv.auditOn(r, hostIDParam(r), "host.trust", strconv.FormatInt(hostA, 10), "")
	if got := lastAuditHost(t, st); got != hostA {
		t.Fatalf("host.trust recorded under %d, want %d", got, hostA)
	}
}
