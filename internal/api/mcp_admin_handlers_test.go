package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// getJSONArray issues a GET and decodes a top-level JSON array (the admin list
// endpoints return arrays, which apiClient.do can't model as a map).
func (a *apiClient) getJSONArray(path string) (int, []map[string]any) {
	a.t.Helper()
	req, _ := http.NewRequest("GET", a.url+path, nil)
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out []map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestMCPAdminOverview drives the admin endpoints over real HTTP: list every
// user's tokens (with usernames) and registered OAuth clients, and revoke/delete
// any of them regardless of owner.
func TestMCPAdminOverview(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})

	ctx := context.Background()
	// admin is user id 1 (created by setup). Add a second user, bob.
	bob, err := a.st.CreateUser(ctx, &store.User{Username: "bob", PasswordHash: "h", Role: "user", Sections: []string{"containers"}})
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _ := a.st.CreateAPIToken(ctx, &store.APIToken{UserID: 1, TokenHash: "hadmin", Name: "admin-tok"})
	bobTok, _ := a.st.CreateAPIToken(ctx, &store.APIToken{UserID: bob, TokenHash: "hbob", Name: "bob-tok", ReadOnly: true})

	// List shows both, each with the owner's username.
	code, toks := a.getJSONArray("/api/mcp-admin/tokens")
	if code != 200 {
		t.Fatalf("list tokens → %d", code)
	}
	byID := map[float64]map[string]any{}
	for _, tk := range toks {
		byID[tk["id"].(float64)] = tk
	}
	if u := byID[float64(adminTok)]; u == nil || u["username"] != "admin" {
		t.Errorf("admin token row wrong: %v", byID[float64(adminTok)])
	}
	if u := byID[float64(bobTok)]; u == nil || u["username"] != "bob" || u["readOnly"] != true {
		t.Errorf("bob token row wrong: %v", byID[float64(bobTok)])
	}

	// Admin revokes bob's token (cross-user — that's the feature).
	if code, _ := a.do("DELETE", "/api/mcp-admin/tokens/"+itoa(bobTok), nil); code != 200 {
		t.Errorf("admin revoke bob token → %d", code)
	}
	// It disappears from the list; revoking again → 404.
	_, toks = a.getJSONArray("/api/mcp-admin/tokens")
	for _, tk := range toks {
		if tk["id"].(float64) == float64(bobTok) {
			t.Error("revoked token still listed")
		}
	}
	if code, _ := a.do("DELETE", "/api/mcp-admin/tokens/"+itoa(bobTok), nil); code != 404 {
		t.Errorf("double revoke → %d (want 404)", code)
	}

	// OAuth clients: register two out-of-band, list, delete one.
	_ = a.st.CreateOAuthClient(ctx, &store.OAuthClient{ID: "dcmcp_a", Name: "Cursor", RedirectURIs: []string{"https://x/cb"}})
	_ = a.st.CreateOAuthClient(ctx, &store.OAuthClient{ID: "dcmcp_b", Name: "Claude", RedirectURIs: []string{"https://y/cb"}})
	code, clients := a.getJSONArray("/api/mcp-admin/oauth-clients")
	if code != 200 || len(clients) != 2 {
		t.Fatalf("list clients → %d len=%d", code, len(clients))
	}
	if code, _ := a.do("DELETE", "/api/mcp-admin/oauth-clients/dcmcp_a", nil); code != 200 {
		t.Errorf("delete client → %d", code)
	}
	if code, _ := a.do("DELETE", "/api/mcp-admin/oauth-clients/dcmcp_a", nil); code != 404 {
		t.Errorf("delete unknown client → %d (want 404)", code)
	}
	_, clients = a.getJSONArray("/api/mcp-admin/oauth-clients")
	if len(clients) != 1 || clients[0]["id"] != "dcmcp_b" {
		t.Errorf("after delete, expected only dcmcp_b: %v", clients)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// loginAs creates a non-admin user and returns a client logged in as them.
func loginAs(t *testing.T, admin *apiClient, username, password string, sections []string, readOnly bool) *apiClient {
	t.Helper()
	if code, _ := admin.do("POST", "/api/users", map[string]any{
		"username": username, "password": password, "role": "user", "readOnly": readOnly, "sections": sections,
	}); code != 200 {
		t.Fatalf("create %s → %d", username, code)
	}
	jar, _ := cookiejar.New(nil)
	c := &apiClient{t: t, c: &http.Client{Jar: jar}, url: admin.url, st: admin.st, dm: admin.dm}
	if code, _ := c.do("POST", "/api/auth/login", map[string]string{"username": username, "password": password}); code != 200 {
		t.Fatalf("%s login → %d", username, code)
	}
	return c
}
