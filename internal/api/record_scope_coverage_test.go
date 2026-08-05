package api

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	cryptopkg "github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Systemic per-HOST authorization coverage for routes addressed by a RECORD id.
//
// The analogue of internal/mcp/tool_host_scope_coverage_test.go, and it exists
// because the REST side lacked one. TestRBACEveryAPIRouteHasASectionDecision
// proves every route decides a *section*; nothing proved that a route which
// resolves a stored record also authorizes against the *host* that record names.
// Those are different questions, and the second one is invisible to the
// permissions middleware: with no `?host=`, it authorizes against host 0, which
// `Grant.HasHost` satisfies unconditionally.
//
// That gap was real. Every `/api/projects/{id}/…` route — including the ones that
// read and rewrite a project's compose file and sidecars — resolved the id and
// acted on it without ever consulting the project's own host, so a role scoped to
// staging could read production credentials out of a project targeting prod and
// rewrite what its next deploy would run.
//
// Ids are sequential SQLite rowids, so "you need the id first" is not a control.
//
// The sweep below finds record-addressed routes itself, from the real router, and
// fails on any it has no decision for — so a new one cannot be added without
// someone saying how it is host-scoped.

// sweptFamilies are the record-addressed prefixes this file drives end to end
// against an out-of-scope record. Everything else must be excused below with a
// reason.
var sweptFamilies = []string{"/api/projects/{id}", "/api/hosts/{id}"}

// hostParamFamilies are the route prefixes that address a DOCKER object, which is
// always named together with the host it lives on (`?host=`). For those the
// permissions middleware already authorizes the right host — the record-scope
// question doesn't arise, because nothing is implied.
//
// A new prefix belongs here only if its routes genuinely carry ?host=; if the
// host comes from a stored row instead, it belongs in the swept set.
var hostParamFamilies = map[string]string{
	"/api/containers": "Docker objects; the host is named in ?host=",
	"/api/volumes":    "Docker objects; the host is named in ?host=",
	"/api/networks":   "Docker objects; the host is named in ?host=",
	"/api/stacks":     "Docker objects; the host is named in ?host=",
	"/api/inspect":    "Docker objects; the host is named in ?host=",
}

// recordRouteDecision explains why a record-addressed route needs no host check,
// for records that carry no host of their own. Anything not listed here, not in a
// ?host= family, and not exercised by the project sweep below fails the test.
var recordRouteDecision = map[string]string{
	// Own tokens: a tokenIssued.Token can only ever narrow its owner's rights, and the owner
	// comes from the session rather than from the path.
	"/api/mcp/tokens/{id}": "own tokens only; ownership checked from session claims",
	// A session belongs to an account, not to a Docker host, and the delete is
	// scoped by the caller's own user id.
	"/api/auth/sessions/{id}": "own sessions only; a session names no host",
	// Fleet-wide MCP administration is admin-only, and admins bypass host scope.
	"/api/mcp-admin/tokens/{id}":        "admin-only prefix",
	"/api/mcp-admin/oauth-clients/{id}": "admin-only prefix",
	// Instance-wide alerting configuration: a rule, a webhook or a parse rule
	// names no host — the alert EVENTS they produce do, and those are scoped.
	"/api/alert-rules/{id}":        "an alert rule is instance-wide; it names no host",
	"/api/alert-rules/{id}/test":   "an alert rule is instance-wide; it names no host",
	"/api/alert-rules/{id}/toggle": "an alert rule is instance-wide; it names no host",
	"/api/webhooks/{id}":           "a webhook is instance-wide; it names no host",
	"/api/parse-rules/{id}":        "a parse rule is instance-wide; it names no host",
	// Installation-level authority, gated by the admin prefix or its own section;
	// none of these rows carry a host column.
	"/api/users/{id}":           "admin-only prefix; users carry no host",
	"/api/users/{id}/password":  "admin-only prefix; users carry no host",
	"/api/roles/{id}":           "admin-only prefix; roles carry no host",
	"/api/roles/{id}/duplicate": "admin-only prefix; roles carry no host",
	"/api/registries/{id}":      "instance-wide credentials; no host column",
	"/api/registries/{id}/test": "instance-wide credentials; no host column",
	// Alert events DO name a host and ARE scoped — asserted separately by
	// TestPentestHostScope_AckAlertIsScopedToTheAlertsHost.
	"/api/alerts/{id}/ack": "scoped against the alert's host; see the ack pentest",
	// Template and builder content lives in the app's data dir and names no
	// Docker host until a project is created from it.
	"/api/project-templates/{id}":           "template content; names no host",
	"/api/project-templates/{id}/files":     "template content; names no host",
	"/api/project-templates/{id}/files/raw": "template content; names no host",
	"/api/project-templates/{id}/files/dir": "template content; names no host",
	"/api/project-templates/{id}/duplicate": "template content; names no host",
	"/api/project-templates/{id}/download":  "template content; names no host",
	"/api/service-blocks/{id}":              "builder content; names no host",
	"/api/service-blocks/{id}/duplicate":    "builder content; names no host",
	"/api/compose-fragments/{id}":           "builder content; names no host",
	"/api/compose-fragments/{id}/duplicate": "builder content; names no host",
}

// TestEveryRecordAddressedRouteDecidesItsHost walks the real router, collects the
// routes addressed by a record id, and requires each to be either exercised by
// the project family below or explicitly excused above.
func TestEveryRecordAddressedRouteDecidesItsHost(t *testing.T) {
	srv := &Server{cfg: config.Config{}}
	h, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("the root handler is not a chi.Routes; cannot enumerate routes")
	}

	var undecided []string
	err := chi.Walk(h, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api") || !strings.Contains(route, "{") {
			return nil
		}
		clean := strings.TrimSuffix(route, "/")
		if clean == "" {
			clean = route
		}
		// The swept families are driven for real below, so they need no entry.
		for _, swept := range sweptFamilies {
			if strings.HasPrefix(clean, swept) {
				return nil
			}
		}
		for prefix := range hostParamFamilies {
			if strings.HasPrefix(clean, prefix+"/") {
				return nil
			}
		}
		if _, ok := recordRouteDecision[clean]; ok {
			return nil
		}
		if _, ok := recordRouteDecision[route]; ok {
			return nil
		}
		undecided = append(undecided, method+" "+clean)
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}
	if len(undecided) > 0 {
		sort.Strings(undecided)
		t.Errorf(`SECURITY: %d record-addressed route(s) have no host-scope decision.

A route that takes a record id and no ?host= is authorized against host 0, which
every grant satisfies — so unless the handler resolves the record's own host, a
caller scoped to one host can act on another's records. Ids are sequential.

Either scope the handler against the record's host (and exercise it, the way the
project family is), or add the route to recordRouteDecision WITH the reason its
record carries no host:
  %s`, len(undecided), strings.Join(undecided, "\n  "))
	}
}

// projectRouteFixture builds a server with a REAL session stack whose caller is
// scoped to host 7, holding projects+hosts writable, plus one project targeting
// host 8 (out of reach). It returns a request-decorator that authenticates as
// that caller, because driving the router means passing the session middleware —
// claims injected straight into the context would skip the very wiring under test.
func recordRouteFixture(t *testing.T) (*Server, map[string]int64, func(*http.Request)) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := t.Context()

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	c, _ := cryptopkg.New(key)
	st.SetCipher(c)

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokenManager(secret, time.Hour)
	srv := &Server{
		cfg:   config.Config{},
		store: st,
		auth:  auth.NewService(st, tokens),
		mw:    auth.NewMiddleware(tokens, st),
	}

	uid, err := st.CreateUser(ctx, &store.User{Username: "scoped", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	roleID, err := st.CreateRole(ctx, &store.Role{
		Name: "Scoped",
		Sections: []store.RoleSection{
			{Section: "projects", Write: true},
			// "hosts" as well, scoped the same way, so the sweep isolates the
			// project-host rule rather than tripping over the separate
			// hosts-section requirement on remote-targeting routes.
			{Section: "hosts", Write: true},
		},
		HostIDs: []int64{7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRoles(ctx, uid, []int64{roleID}); err != nil {
		t.Fatal(err)
	}

	u, err := st.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	token := issueTestSession(t, tokens, st, u)

	projectID, err := st.CreateProject(ctx, &store.Project{Name: "prod-api", Slug: "prod-api", HostID: 8})
	if err != nil {
		t.Fatal(err)
	}
	// A second host the caller cannot reach, addressed by /api/hosts/{id}.
	hostID, err := st.CreateHost(ctx, &store.Host{Name: "prod", Kind: "tcp", Address: "tcp://10.0.0.8:2376"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{"/api/projects/{id}": projectID, "/api/hosts/{id}": hostID}
	return srv, ids, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	}
}

// PENTEST: drive every record-addressed route the router actually exposes against
// a record on an out-of-scope host and require a refusal.
//
// Driven rather than asserted through the helper, because the helper being right
// says nothing about whether each handler calls it. It also pins a second
// property worth keeping: the refusal arrives BEFORE the body is parsed or the
// filesystem is touched, so a malformed request to an out-of-reach record cannot
// be told apart from a well-formed one.
//
// The hosts family is here because this very sweep found it: /api/hosts/{id}
// names its host in the path, so the middleware was authorizing the `hosts`
// section against host 0. A role scoped to one host could disable, delete or —
// worst — pin a new SSH host key for another.
func TestPentestRecordRoutes_OutOfScopeHostIsRefused(t *testing.T) {
	srv, ids, authenticate := recordRouteFixture(t)
	router, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("the root handler is not a chi.Routes")
	}
	handler := srv.Handler()

	checked := map[string]int{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		family := ""
		for _, swept := range sweptFamilies {
			if strings.HasPrefix(route, swept) {
				family = swept
			}
		}
		if family == "" {
			return nil
		}
		path := strings.ReplaceAll(strings.TrimSuffix(route, "/"), "{id}", strconv.FormatInt(ids[family], 10))
		t.Run(method+" "+route, func(t *testing.T) {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			authenticate(req)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			// 404, not 403: a record on a host you cannot reach must be
			// indistinguishable from one that does not exist, or the id space
			// becomes a directory of what runs elsewhere.
			if rec.Code != http.StatusNotFound {
				t.Errorf("SECURITY: %s %s on an out-of-scope record → %d, want 404\nbody: %s",
					method, route, rec.Code, strings.TrimSpace(rec.Body.String()))
			}
		})
		checked[family]++
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}
	// Guards the guard: if the walk stops matching (routes moved or renamed) the
	// sweep would silently check nothing and still pass.
	for _, family := range sweptFamilies {
		if checked[family] == 0 {
			t.Fatalf("no routes exercised for %s — the sweep is no longer finding them", family)
		}
	}
	if total := checked["/api/projects/{id}"]; total < 10 {
		t.Fatalf("only %d project routes were exercised — the sweep is no longer finding them", total)
	}
}

// The same routes must still work for records the caller CAN reach. A sweep that
// only proves refusals is one `return 404` away from passing while the feature is
// broken — which is the failure mode this file would otherwise invite.
func TestRecordRoutes_InScopeRecordsStillReachable(t *testing.T) {
	srv, st, u := scopedFixture(t, "projects", 7)
	ctx := t.Context()
	id, err := st.CreateProject(ctx, &store.Project{Name: "staging-api", Slug: "staging-api", HostID: 7})
	if err != nil {
		t.Fatal(err)
	}

	// Called through the helper rather than the router so the fixture stays the
	// same shape as the rest of this package's host-scope tests; loadProject reads
	// the id from the chi route context, so that has to be attached.
	req := httptest.NewRequest("GET", "/api/projects/"+strconv.FormatInt(id, 10)+"/files", nil).
		WithContext(chiParam(ctxAs(u.ID, "user"), "id", strconv.FormatInt(id, 10)))
	rec := httptest.NewRecorder()
	if _, ok := srv.loadProject(rec, req); !ok {
		t.Fatalf("a project on the caller's own host must stay reachable, got %d: %s",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}

// And the same for a host the caller is scoped to: scopedHostID must allow it.
func TestRecordRoutes_InScopeHostStillReachable(t *testing.T) {
	srv, st, u := scopedFixture(t, "hosts", 7)
	ctx := t.Context()

	// Seeded local host (id 1) plus two more, so the ids are unambiguous.
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	inScope, err := st.CreateHost(ctx, &store.Host{Name: "staging", Kind: "tcp", Address: "tcp://10.0.0.7:2376"})
	if err != nil {
		t.Fatal(err)
	}
	outOfScope, err := st.CreateHost(ctx, &store.Host{Name: "prod", Kind: "tcp", Address: "tcp://10.0.0.8:2376"})
	if err != nil {
		t.Fatal(err)
	}
	// Re-point the caller's role at the host that actually exists.
	roles, _ := st.RoleIDsForUser(ctx, u.ID)
	if err := st.UpdateRole(ctx, roles[0], "Scoped", "",
		[]store.RoleSection{{Section: "hosts", Write: true}}, []int64{inScope}); err != nil {
		t.Fatal(err)
	}

	req := func(id int64) *http.Request {
		r := httptest.NewRequest("PATCH", "/api/hosts/"+strconv.FormatInt(id, 10), nil)
		return r.WithContext(chiParam(ctxAs(u.ID, "user"), "id", strconv.FormatInt(id, 10)))
	}
	if _, ok := srv.scopedHostID(httptest.NewRecorder(), req(inScope), true); !ok {
		t.Error("the caller's own host must stay reachable")
	}
	rec := httptest.NewRecorder()
	if _, ok := srv.scopedHostID(rec, req(outOfScope), true); ok {
		t.Error("SECURITY: an out-of-scope host was accepted")
	} else if rec.Code != http.StatusNotFound {
		t.Errorf("out-of-scope host should answer 404 (like missing), got %d", rec.Code)
	}
}

// chiParam attaches a chi URL parameter, which handlers read via chi.URLParam.
func chiParam(ctx context.Context, key, value string) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}
