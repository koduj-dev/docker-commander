package api

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
)

// End-to-end RBAC: a real signed-in session driven through the real router, so
// route mapping, middleware mounting, role resolution and the write-intent rules
// are all exercised together rather than asserted in isolation.
//
// This is feasible because the localhost 2FA exemption lets a test account log in
// with a password alone — the same trick the WebSocket pentest uses.
//
// These run under -short (so in CI): they need no Docker daemon. Requests that
// would reach one only ever get asserted on 403-vs-not, and a daemon error is a
// perfectly good "the gate let it through".

// rbacFixture spins up the server, completes first-run setup as admin, and enables
// the localhost 2FA exemption so restricted accounts can sign in.
func rbacFixture(t *testing.T) *apiClient {
	t.Helper()
	admin := newAPI(t)
	if code, _ := admin.do("POST", "/api/auth/setup", map[string]string{
		"username": "admin", "password": "correcthorse123",
	}); code != 200 {
		t.Fatalf("setup: %d", code)
	}
	if code, _ := admin.do("PUT", "/api/settings", map[string]any{
		"disabledSections": []string{}, "localhostNo2fa": true,
	}); code != 200 {
		t.Fatalf("enable localhost 2FA exemption: %d", code)
	}
	return admin
}

// loginWithRoles creates an account WITH role assignments and returns a client
// signed in as it. (The shared loginAs helper predates roles and can't assign any.)
func loginWithRoles(t *testing.T, admin *apiClient, name string, readOnly bool, sections []string, roleIDs []int64) *apiClient {
	t.Helper()
	body := map[string]any{
		"username": name, "password": "restricted123", "role": "user",
		"readOnly": readOnly, "sections": sections,
	}
	if roleIDs != nil {
		body["roleIds"] = roleIDs
	}
	if code, resp := admin.do("POST", "/api/users", body); code != 200 {
		t.Fatalf("create %s: %d %v", name, code, resp)
	}
	jar, _ := cookiejar.New(nil)
	c := &apiClient{t: t, c: &http.Client{Jar: jar}, url: admin.url, dm: admin.dm, st: admin.st}
	if code, _ := c.do("POST", "/api/auth/login", map[string]string{
		"username": name, "password": "restricted123",
	}); code != 200 {
		t.Fatalf("%s login: %d", name, code)
	}
	return c
}

// TestRBACEndToEndRawInspectIsDenied is the end-to-end proof of the inspect fix:
// a signed-in account with NO grants at all must be refused, over the real router,
// rather than reaching the raw payload (which for a container carries Config.Env).
func TestRBACEndToEndRawInspectIsDenied(t *testing.T) {
	admin := rbacFixture(t)
	nobody := loginWithRoles(t, admin, "nobody", true, nil, nil)

	for _, kind := range []string{"container", "image", "volume", "network"} {
		code, _ := nobody.do("GET", "/api/inspect/"+kind+"?id=whatever", nil)
		if code != http.StatusForbidden {
			t.Errorf("SECURITY: zero-grant account got %d on /api/inspect/%s, want 403", code, kind)
		}
	}

	// A holder of the owning section is not permission-denied. Without a daemon the
	// call fails downstream instead — anything but 403 proves the gate let it past.
	viewer := loginWithRoles(t, admin, "imgviewer", false, []string{"images"}, nil)
	if code, _ := viewer.do("GET", "/api/inspect/image?id=whatever", nil); code == http.StatusForbidden {
		t.Error("an images grant should permit raw image inspect")
	}
	// …but that grant must not extend to another kind.
	if code, _ := viewer.do("GET", "/api/inspect/container?id=whatever", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: an images grant reached container inspect (%d)", code)
	}
}

// TestRBACEndToEndRoleGrantsOverHTTP drives role-derived access through the real
// stack: a role is the only source of the user's permissions here, so this proves
// the middleware resolves roles (not just the per-account section list).
func TestRBACEndToEndRoleGrantsOverHTTP(t *testing.T) {
	admin := rbacFixture(t)

	// A writable role and a read-only one, created over the API as an admin would.
	code, resp := admin.do("POST", "/api/roles", map[string]any{
		"name":     "ContainersRW",
		"sections": []map[string]any{{"section": "containers", "write": true}},
	})
	if code != 200 {
		t.Fatalf("create writable role: %d %v", code, resp)
	}
	rwID := int64(resp["id"].(float64))

	code, resp = admin.do("POST", "/api/roles", map[string]any{
		"name":     "ContainersRO",
		"sections": []map[string]any{{"section": "containers", "write": false}},
	})
	if code != 200 {
		t.Fatalf("create read-only role: %d %v", code, resp)
	}
	roID := int64(resp["id"].(float64))

	// No per-account sections at all — everything comes from the role.
	rw := loginWithRoles(t, admin, "rwuser", false, nil, []int64{rwID})
	ro := loginWithRoles(t, admin, "rouser", false, nil, []int64{roID})

	// Reads: both allowed (not 403).
	for name, c := range map[string]*apiClient{"writable": rw, "read-only": ro} {
		if code, _ := c.do("GET", "/api/containers", nil); code == http.StatusForbidden {
			t.Errorf("%s role was denied a read on /api/containers", name)
		}
	}

	// Writes: only the writable role. This is the per-section write bit enforced
	// through the real middleware, which no other test does.
	if code, _ := ro.do("POST", "/api/containers/abc/restart", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: a read-only role performed a write (%d)", code)
	}
	if code, _ := rw.do("POST", "/api/containers/abc/restart", nil); code == http.StatusForbidden {
		t.Error("a writable role should be permitted the write (a daemon error is fine)")
	}

	// Neither role touches a section it never granted, nor the admin surface.
	for name, c := range map[string]*apiClient{"writable": rw, "read-only": ro} {
		if code, _ := c.do("GET", "/api/hosts", nil); code != http.StatusForbidden {
			t.Errorf("SECURITY: the %s role reached /api/hosts (%d)", name, code)
		}
		if code, _ := c.do("GET", "/api/roles", nil); code != http.StatusForbidden {
			t.Errorf("SECURITY: the %s role reached role management (%d)", name, code)
		}
		if code, _ := c.do("GET", "/api/users", nil); code != http.StatusForbidden {
			t.Errorf("SECURITY: the %s role reached user management (%d)", name, code)
		}
	}
}

// TestRBACEndToEndRevocationIsImmediate proves over HTTP that removing a role takes
// effect on the very next request — the session carries no cached grants.
func TestRBACEndToEndRevocationIsImmediate(t *testing.T) {
	admin := rbacFixture(t)

	code, resp := admin.do("POST", "/api/roles", map[string]any{
		"name":     "Temp",
		"sections": []map[string]any{{"section": "containers", "write": true}},
	})
	if code != 200 {
		t.Fatalf("create role: %d %v", code, resp)
	}
	roleID := int64(resp["id"].(float64))

	user := loginWithRoles(t, admin, "temp", false, nil, []int64{roleID})
	if code, _ := user.do("GET", "/api/containers", nil); code == http.StatusForbidden {
		t.Fatal("setup: the role should grant containers")
	}

	// Find the account and strip its roles, leaving the session untouched.
	var uid int64
	_, users := admin.getJSONArray("/api/users")
	for _, m := range users {
		if m["username"] == "temp" {
			uid = int64(m["id"].(float64))
		}
	}
	if uid == 0 {
		t.Fatal("could not find the test account")
	}
	if code, _ := admin.do("PATCH", "/api/users/"+itoa(uid), map[string]any{
		"role": "user", "readOnly": false, "sections": []string{}, "roleIds": []int64{},
	}); code != 200 {
		t.Fatalf("revoke roles: %d", code)
	}

	// Same cookie, next request: denied.
	if code, _ := user.do("GET", "/api/containers", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: access survived revocation on an existing session (%d)", code)
	}
}

// PENTEST: the account-level read-only flag must hold over HTTP even when a role
// grants write — the store-level test asserts the grant computation, this asserts
// the request actually gets a 403.
func TestPentestRBACEndToEndReadOnlyAccountBeatsWritableRole(t *testing.T) {
	admin := rbacFixture(t)

	code, resp := admin.do("POST", "/api/roles", map[string]any{
		"name":     "Writer",
		"sections": []map[string]any{{"section": "containers", "write": true}},
	})
	if code != 200 {
		t.Fatalf("create role: %d %v", code, resp)
	}
	roleID := int64(resp["id"].(float64))

	ro := loginWithRoles(t, admin, "capped", true /* readOnly */, nil, []int64{roleID})
	if code, _ := ro.do("GET", "/api/containers", nil); code == http.StatusForbidden {
		t.Error("a read-only account should still read what the role grants")
	}
	if code, _ := ro.do("POST", "/api/containers/abc/restart", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: a read-only ACCOUNT wrote via a writable role (%d)", code)
	}
	// The privileged GETs are writes too, and must be refused just the same.
	for _, p := range []string{"/api/containers/abc/exec", "/api/images/pull", "/api/containers/bulk-pull?ids=abc"} {
		if code, _ := ro.do("GET", p, nil); code != http.StatusForbidden {
			t.Errorf("SECURITY: a read-only account reached the privileged GET %s (%d)", p, code)
		}
	}
}

// TestRBACEndToEndBulkContainerAction proves the bulk endpoint reuses the
// existing "containers" section write permission — no separate permission
// model for bulk actions, per the roadmap's narrowed v1 scope. A read-only
// role must be refused; a writable one must reach the handler (a daemon error
// for the fake ids is fine — the point is that RBAC itself let the request
// through, same as every other write test in this file).
func TestRBACEndToEndBulkContainerAction(t *testing.T) {
	admin := rbacFixture(t)

	code, resp := admin.do("POST", "/api/roles", map[string]any{
		"name":     "ContainersRW2",
		"sections": []map[string]any{{"section": "containers", "write": true}},
	})
	if code != 200 {
		t.Fatalf("create writable role: %d %v", code, resp)
	}
	rwID := int64(resp["id"].(float64))

	code, resp = admin.do("POST", "/api/roles", map[string]any{
		"name":     "ContainersRO2",
		"sections": []map[string]any{{"section": "containers", "write": false}},
	})
	if code != 200 {
		t.Fatalf("create read-only role: %d %v", code, resp)
	}
	roID := int64(resp["id"].(float64))

	rw := loginWithRoles(t, admin, "bulkrw", false, nil, []int64{rwID})
	ro := loginWithRoles(t, admin, "bulkro", false, nil, []int64{roID})

	body := map[string]any{"ids": []string{"abc"}, "action": "stop"}
	if code, _ := ro.do("POST", "/api/containers/bulk-action", body); code != http.StatusForbidden {
		t.Errorf("SECURITY: a read-only role performed a bulk write (%d)", code)
	}
	if code, _ := rw.do("POST", "/api/containers/bulk-action", body); code == http.StatusForbidden {
		t.Error("a writable role should be permitted the bulk write (a daemon error is fine)")
	}

	// Zero-grant account: refused outright, same as every other containers write.
	nobody := loginWithRoles(t, admin, "bulknobody", true, nil, nil)
	if code, _ := nobody.do("POST", "/api/containers/bulk-action", body); code != http.StatusForbidden {
		t.Errorf("SECURITY: a zero-grant account performed a bulk write (%d)", code)
	}
}

// TestRBACEndToEndBulkPullImages proves /containers/bulk-pull requires BOTH
// "containers" write (the middleware's route-level gate — it names which
// containers to resolve images for) AND "images" write (checked inside the
// handler itself — the pull attaches a registry credential and mutates the
// shared image store, the same capability /images/pull needs "images" write
// for). Neither section alone is enough: a containers-only role must not
// gain a pull capability just because this route hangs off /containers, and
// an images-only role must not reach it without also being able to select
// containers on this host.
func TestRBACEndToEndBulkPullImages(t *testing.T) {
	admin := rbacFixture(t)

	mkRole := func(name string, sections []map[string]any) int64 {
		code, resp := admin.do("POST", "/api/roles", map[string]any{"name": name, "sections": sections})
		if code != 200 {
			t.Fatalf("create role %s: %d %v", name, code, resp)
		}
		return int64(resp["id"].(float64))
	}
	containersOnlyID := mkRole("ContainersOnlyPull", []map[string]any{{"section": "containers", "write": true}})
	imagesOnlyID := mkRole("ImagesOnlyPull", []map[string]any{{"section": "images", "write": true}})
	bothID := mkRole("ContainersAndImagesPull", []map[string]any{
		{"section": "containers", "write": true}, {"section": "images", "write": true},
	})
	containersRWImagesROID := mkRole("ContainersRWImagesRO", []map[string]any{
		{"section": "containers", "write": true}, {"section": "images", "write": false},
	})

	containersOnly := loginWithRoles(t, admin, "pullcontainersonly", false, nil, []int64{containersOnlyID})
	imagesOnly := loginWithRoles(t, admin, "pullimagesonly", false, nil, []int64{imagesOnlyID})
	both := loginWithRoles(t, admin, "pullboth", false, nil, []int64{bothID})
	imagesReadOnly := loginWithRoles(t, admin, "pullimgro", false, nil, []int64{containersRWImagesROID})

	// The pre-DC-SEC-002 shape: containers write alone must now be refused —
	// this is the exact gap the fix closes, so it's the one case here worth
	// naming SECURITY on failure.
	if code, _ := containersOnly.do("GET", "/api/containers/bulk-pull", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: a containers-only role reached bulk-pull (%d) — it must also need images write", code)
	}
	if code, _ := imagesOnly.do("GET", "/api/containers/bulk-pull", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: an images-only role reached bulk-pull (%d) — it must also need containers write", code)
	}
	// Read access to images isn't write access: a role with containers write
	// but only images READ must still be refused.
	if code, _ := imagesReadOnly.do("GET", "/api/containers/bulk-pull", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: a role with images read-only (not write) reached bulk-pull (%d)", code)
	}
	// Holding both is not permission-denied (a validation/daemon error for no
	// ids sent is fine over a plain GET with no WS handshake — the point is
	// RBAC itself let the request through).
	if code, _ := both.do("GET", "/api/containers/bulk-pull", nil); code == http.StatusForbidden {
		t.Error("a role with both containers and images write should be permitted to reach bulk-pull")
	}

	nobody := loginWithRoles(t, admin, "pullnobody", true, nil, nil)
	if code, _ := nobody.do("GET", "/api/containers/bulk-pull", nil); code != http.StatusForbidden {
		t.Errorf("SECURITY: a zero-grant account reached bulk-pull (%d)", code)
	}
}
