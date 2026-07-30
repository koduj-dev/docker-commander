package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Systemic RBAC coverage.
//
// The per-feature tests each check a rule they already know about. What they
// cannot catch is the class of hole where the *wiring* is wrong or missing — a new
// route with no section mapping is silently ungated, a role grants something that
// was never meant to be grantable, or a "write" that arrives as a GET slips past a
// read-only grant. Those are the failures that reach production green, so they get
// tests that fail on *absence* rather than on a specific known bug.

// ungatedRoutes are the /api paths deliberately reachable by any authenticated
// user. Anything not on this list must map to a section, so adding a route forces
// a decision instead of defaulting to open.
//
// If a new route legitimately belongs here, add it WITH a reason.
var ungatedRoutes = map[string]string{
	// Auth + session: needed before/independent of any grant.
	"/api/auth/status":      "public auth probe",
	"/api/auth/setup":       "first-run setup",
	"/api/auth/login":       "login",
	"/api/auth/2fa":         "2FA challenge",
	"/api/auth/logout":      "any signed-in user may log out",
	"/api/auth/me":          "own identity",
	"/api/auth/totp/setup":  "own 2FA enrolment",
	"/api/auth/totp/enable": "own 2FA enrolment",
	// Writes the CALLER's own alert address, taken from their session claims —
	// it cannot touch another account (TestPentestSetMyEmail_OnlyAffectsTheCaller).
	"/api/auth/me/email": "own alert address",
	// Reads only the caller's own roles and grants
	// (TestPentestMyAccess_OnlyOwnData).
	"/api/auth/me/access": "own permissions overview",
	// Self-service MCP tokens: a token can only narrow its owner's own rights.
	"/api/mcp/status":      "own MCP availability",
	"/api/mcp/tokens":      "own tokens only",
	"/api/mcp/tokens/{id}": "own tokens only",
	// Shared reads that carry no host/container authority of their own.
	"/api/system":          "version/health for the shell",
	"/api/system/df":       "aggregate disk usage; no per-object detail",
	"/api/version":         "app version for the shell",
	"/api/stats/overview":  "counts for the dashboard shell",
	"/api/stats/ports":     "published-port map used by the shell's port hints",
	"/api/metrics/history": "per-container CPU/mem series (no config, no env)",
	"/api/ws":              "gated per subscription channel (see wsChannelSection)",
	"/api/prefs":           "own UI preferences",
	"/api/metrics":         "separate metrics-token auth",
	"/api/notifications":   "own in-app feed",
	"/api/oauth/*":         "MCP OAuth authorization server (own flow)",
	"/api/.well-known/*":   "OAuth/protected-resource metadata",
	"/api/mcp":             "bearer-authenticated MCP transport (own principal)",
	"/api/mcp/*":           "bearer-authenticated MCP transport (own principal)",
	"/api/update/restart":  "admin-only via the /update prefix",
}

// TestRBACEveryAPIRouteHasASectionDecision walks the REAL router and asserts each
// /api route either maps to a section or is on the explicit ungated allowlist.
//
// This is the test that would have caught "a new endpoint is public by accident":
// sectionForPath falls through to "" (ungated) for anything it doesn't recognise,
// so a route added under a new prefix is open to every signed-in user with no
// error anywhere.
func TestRBACEveryAPIRouteHasASectionDecision(t *testing.T) {
	srv := &Server{cfg: config.Config{}}
	h, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("the root handler is not a chi.Routes; cannot enumerate routes")
	}

	var undecided []string
	err := chi.Walk(h, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api") {
			return nil // SPA/static routes carry no RBAC
		}
		clean := strings.TrimSuffix(route, "/")
		if clean == "" {
			clean = route
		}
		if _, allowed := ungatedRoutes[clean]; allowed {
			return nil
		}
		if _, allowed := ungatedRoutes[route]; allowed {
			return nil
		}
		if sectionForPath(route) == "" {
			undecided = append(undecided, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}
	if len(undecided) > 0 {
		sort.Strings(undecided)
		t.Errorf(`SECURITY: %d /api route(s) are ungated and not on the allowlist.
Any signed-in user can reach them. Either map the prefix in sectionForPath or add
the route to ungatedRoutes WITH a reason:
  %s`, len(undecided), strings.Join(undecided, "\n  "))
	}
}

// PENTEST: "__admin" is not a real section, so no role and no per-user list may
// grant it. If it ever became grantable, holding it would mean user management,
// settings and LDAP — i.e. total control.
func TestPentestAdminSectionIsNotGrantable(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	if store.ValidSection("__admin") {
		t.Fatal("SECURITY: __admin is a valid section, so it can be granted")
	}

	// Via a role…
	roleID, err := st.CreateRole(ctx, &store.Role{
		Name: "Sneaky", Sections: []store.RoleSection{{Section: "__admin", Write: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	role, _ := st.RoleByID(ctx, roleID)
	if len(role.Sections) != 0 {
		t.Errorf("SECURITY: a role stored an __admin grant: %+v", role.Sections)
	}

	// …and via the per-user list.
	uid, _ := st.CreateUser(ctx, &store.User{
		Username: "sneak", Role: "user", Sections: []string{"__admin"},
	})
	_ = st.SetUserRoles(ctx, uid, []int64{roleID})
	u, _ := st.UserByID(ctx, uid)

	grants, err := st.EffectiveGrants(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grants["__admin"]; ok {
		t.Error("SECURITY: __admin appeared in effective grants")
	}
	if err := srv.checkAccess(ctx, u, "__admin", false, 0); err == nil {
		t.Error("SECURITY: a non-admin holding a bogus __admin grant reached the admin section")
	}
}

// PENTEST: a few privileged actions arrive as GETs (exec, pull, push, scan) and
// isWriteRequest classifies them as writes. With per-section write bits, a
// READ-ONLY ROLE must block them — this is the combination the per-feature tests
// miss, since they only exercised the account-level read-only flag.
func TestPentestReadOnlyRoleBlocksGetWrites(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	uid, _ := st.CreateUser(ctx, &store.User{Username: "looker", Role: "user"})
	roleID, err := st.CreateRole(ctx, &store.Role{
		Name: "ContainersRO",
		Sections: []store.RoleSection{
			{Section: "containers", Write: false},
			{Section: "images", Write: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRoles(ctx, uid, []int64{roleID}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(ctx, uid)

	// Each of these is a GET that isWriteRequest must call a write, and which the
	// read-only role must then be denied.
	for _, p := range []string{
		"/api/containers/abc/exec",
		"/api/images/pull",
		"/api/images/push",
		"/api/images/scan",
	} {
		r := httptest.NewRequest("GET", p, nil)
		if !isWriteRequest(r) {
			t.Errorf("SECURITY: GET %s is not classified as a write, so a read-only grant would let it through", p)
			continue
		}
		section := sectionForPath(p)
		if section == "" {
			t.Errorf("SECURITY: %s maps to no section", p)
			continue
		}
		if err := srv.checkAccess(ctx, u, section, true, 0); err == nil {
			t.Errorf("SECURITY: a read-only role allowed the privileged GET %s", p)
		}
		// The same path as a plain read stays allowed.
		if err := srv.checkAccess(ctx, u, section, false, 0); err != nil {
			t.Errorf("reads on %s should still work: %v", section, err)
		}
	}
}

// PENTEST: one user's role must not leak into another's grants. Cheap to get wrong
// with a mis-keyed join, and invisible in single-user tests.
func TestPentestRoleGrantsDoNotLeakBetweenUsers(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	privileged, _ := st.CreateUser(ctx, &store.User{Username: "ops", Role: "user"})
	other, _ := st.CreateUser(ctx, &store.User{Username: "dev", Role: "user"})
	roleID, _ := st.CreateRole(ctx, &store.Role{
		Name: "HostsAdmin", Sections: []store.RoleSection{{Section: "hosts", Write: true}},
	})
	if err := st.SetUserRoles(ctx, privileged, []int64{roleID}); err != nil {
		t.Fatal(err)
	}

	pu, _ := st.UserByID(ctx, privileged)
	ou, _ := st.UserByID(ctx, other)

	pg, _ := st.EffectiveGrants(ctx, pu)
	if !pg["hosts"].Granted || !pg["hosts"].Write {
		t.Fatalf("setup: the assigned user should hold hosts: %+v", pg)
	}
	og, _ := st.EffectiveGrants(ctx, ou)
	if len(og) != 0 {
		t.Errorf("SECURITY: an unassigned user inherited grants: %+v", og)
	}
}

// Every section, both write intents, through the real gate. A matrix rather than
// spot checks, so a section that behaves differently from the rest shows up.
func TestRBACSectionMatrix(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	for _, section := range store.Sections {
		t.Run(section, func(t *testing.T) {
			// A role granting exactly this section, writable.
			rw, err := st.CreateRole(ctx, &store.Role{
				Name: "rw-" + section, Sections: []store.RoleSection{{Section: section, Write: true}},
			})
			if err != nil {
				t.Fatal(err)
			}
			ro, err := st.CreateRole(ctx, &store.Role{
				Name: "ro-" + section, Sections: []store.RoleSection{{Section: section, Write: false}},
			})
			if err != nil {
				t.Fatal(err)
			}

			rwUser, _ := st.CreateUser(ctx, &store.User{Username: "rw-" + section, Role: "user"})
			roUser, _ := st.CreateUser(ctx, &store.User{Username: "ro-" + section, Role: "user"})
			noneUser, _ := st.CreateUser(ctx, &store.User{Username: "none-" + section, Role: "user"})
			_ = st.SetUserRoles(ctx, rwUser, []int64{rw})
			_ = st.SetUserRoles(ctx, roUser, []int64{ro})

			rwU, _ := st.UserByID(ctx, rwUser)
			roU, _ := st.UserByID(ctx, roUser)
			noneU, _ := st.UserByID(ctx, noneUser)

			if err := srv.checkAccess(ctx, rwU, section, true, 0); err != nil {
				t.Errorf("writable role denied a write on %s: %v", section, err)
			}
			if err := srv.checkAccess(ctx, roU, section, false, 0); err != nil {
				t.Errorf("read-only role denied a read on %s: %v", section, err)
			}
			if err := srv.checkAccess(ctx, roU, section, true, 0); err == nil {
				t.Errorf("SECURITY: read-only role allowed a write on %s", section)
			}
			if err := srv.checkAccess(ctx, noneU, section, false, 0); err == nil {
				t.Errorf("SECURITY: a user with no grants read %s", section)
			}
			// No role may reach the admin section.
			if err := srv.checkAccess(ctx, rwU, "__admin", false, 0); err == nil {
				t.Errorf("SECURITY: the %s role reached __admin", section)
			}
		})
	}
}

// The gate must deny — not allow — when grants can't be established. Verified by
// closing the store under it, which is the only way to force the error path.
func TestRBACFailsClosedOnStoreError(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	uid, _ := st.CreateUser(ctx, &store.User{
		Username: "u", Role: "user", Sections: []string{"containers"},
	})
	u, _ := st.UserByID(ctx, uid)
	srv := &Server{cfg: config.Config{}, store: st}
	if err := srv.checkAccess(ctx, u, "containers", false, 0); err != nil {
		t.Fatalf("setup: access should work before the store dies: %v", err)
	}

	st.Close()
	if err := srv.checkAccess(ctx, u, "containers", false, 0); err == nil {
		t.Error("SECURITY: access was granted while grants could not be computed — the gate must fail closed")
	}
}

// PENTEST: /api/inspect/{kind} returns the RAW docker inspect payload, and for a
// container that includes Config.Env — database passwords, API keys. It used to be
// ungated, so any signed-in account (zero sections, even read-only) could read the
// environment of any container on any host via ?host=N. It must now be gated by
// the section owning the kind.
func TestPentestRawInspectRequiresTheOwningSection(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	nobody, _ := st.CreateUser(ctx, &store.User{Username: "nobody", Role: "user", ReadOnly: true})
	nu, _ := st.UserByID(ctx, nobody)

	for path, want := range map[string]string{
		"/api/inspect/container": "containers",
		"/api/inspect/image":     "images",
		"/api/inspect/volume":    "volumes",
		"/api/inspect/network":   "networks",
	} {
		got := sectionForPath(path)
		if got != want {
			t.Errorf("SECURITY: %s maps to %q, want %q", path, got, want)
		}
		if err := srv.checkAccess(ctx, nu, got, false, 0); err == nil {
			t.Errorf("SECURITY: a zero-grant account can raw-inspect via %s", path)
		}
	}

	// An unknown kind must fail closed on the most privileged section rather than
	// becoming readable by everyone.
	if got := sectionForPath("/api/inspect/somethingnew"); got != "containers" {
		t.Errorf("SECURITY: an unknown inspect kind maps to %q, want the fail-closed \"containers\"", got)
	}

	// And the legitimate case still works: holding the section allows the read.
	roleID, _ := st.CreateRole(ctx, &store.Role{
		Name: "CtrRO", Sections: []store.RoleSection{{Section: "containers"}},
	})
	viewer, _ := st.CreateUser(ctx, &store.User{Username: "viewer", Role: "user"})
	_ = st.SetUserRoles(ctx, viewer, []int64{roleID})
	vu, _ := st.UserByID(ctx, viewer)
	if err := srv.checkAccess(ctx, vu, sectionForPath("/api/inspect/container"), false, 0); err != nil {
		t.Errorf("a containers grant should allow raw container inspect: %v", err)
	}
}

// TestRBACGatedRoutesActuallyMountThePermissionsMiddleware closes a gap in the
// route-mapping test above: that one proves each route MAPS to a section, not that
// the gate is in its chain. A route group registered without `r.Use(s.permissions)`
// would map correctly and still be unenforced — mapping and mounting are separate
// failures, and only one of them was covered.
func TestRBACGatedRoutesActuallyMountThePermissionsMiddleware(t *testing.T) {
	srv := &Server{cfg: config.Config{}}
	want := reflect.ValueOf(srv.permissions).Pointer()
	h, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("the root handler is not a chi.Routes")
	}

	var unguarded []string
	err := chi.Walk(h, func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api") || sectionForPath(route) == "" {
			return nil // ungated routes are covered by the allowlist test
		}
		for _, mw := range mws {
			if reflect.ValueOf(mw).Pointer() == want {
				return nil
			}
		}
		unguarded = append(unguarded, method+" "+route)
		return nil
	})
	// Since phase 2 this test guards the HOST check too: ?host= is authorised in
	// that same middleware, so a gated route missing it is unscoped as well as
	// ungated.
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}
	if len(unguarded) > 0 {
		sort.Strings(unguarded)
		t.Errorf(`SECURITY: %d route(s) map to a section but do NOT have the permissions
middleware in their chain, so the mapping is never enforced:
  %s`, len(unguarded), strings.Join(unguarded, "\n  "))
	}
}

// PENTEST: an unrecognised WebSocket channel must fail closed. The hub authorises
// per channel, so a channel added without a mapping would otherwise stream to
// anyone holding a session.
func TestPentestWSUnknownChannelFailsClosed(t *testing.T) {
	for _, ch := range []string{"", "exec", "events", "files", "anything-new"} {
		if section, ok := wsChannelSection(ch); ok {
			t.Errorf("SECURITY: unknown ws channel %q was authorised against section %q", ch, section)
		}
	}
	// The two known channels still resolve, or the streams break entirely.
	for _, ch := range []string{"stats", "logs"} {
		section, ok := wsChannelSection(ch)
		if !ok || section != "containers" {
			t.Errorf("ws channel %q = (%q, %v), want (containers, true)", ch, section, ok)
		}
	}
}

// PENTEST: the SMTP config is a single instance-wide mail relay with a stored
// credential, so it is admin-only. It used to be gated by the "alerts" section,
// which let any non-admin holding that section repoint the whole instance's
// outbound mail to a server they control — and thereby receive its notifications.
// The password is never returned by the API, so this is about redirection and
// takeover of delivery, not credential theft.
func TestPentestSMTPConfigIsAdminOnly(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	for _, p := range []string{"/api/smtp", "/api/smtp/test"} {
		if got := sectionForPath(p); got != "__admin" {
			t.Errorf("SECURITY: %s maps to %q, want \"__admin\"", p, got)
		}
	}

	// A user holding alerts (writable, via a role) must be refused.
	uid, _ := st.CreateUser(ctx, &store.User{Username: "alerter", Role: "user"})
	roleID, err := st.CreateRole(ctx, &store.Role{
		Name: "Alerter", Sections: []store.RoleSection{{Section: "alerts", Write: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRoles(ctx, uid, []int64{roleID}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(ctx, uid)

	if err := srv.checkAccess(ctx, u, sectionForPath("/api/smtp"), true, 0); err == nil {
		t.Error("SECURITY: an alerts-only account can still rewrite the instance SMTP relay")
	}
	// …while the rest of the alerts surface still works for them, so the change is
	// narrowly scoped rather than breaking alerting for non-admins.
	for _, p := range []string{"/api/alerts", "/api/alert-rules", "/api/webhooks"} {
		if err := srv.checkAccess(ctx, u, sectionForPath(p), true, 0); err != nil {
			t.Errorf("an alerts role should still manage %s: %v", p, err)
		}
	}

	admin, _ := st.CreateUser(ctx, &store.User{Username: "root", Role: "admin"})
	au, _ := st.UserByID(ctx, admin)
	if err := srv.checkAccess(ctx, au, sectionForPath("/api/smtp"), true, 0); err != nil {
		t.Errorf("an admin must still configure SMTP: %v", err)
	}
}
