package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// These are end-to-end "runtime smoke" tests: they drive the REAL streamable
// HTTP transport with the REAL MCP SDK client over a real (loopback) socket,
// through the bearer auth middleware and the tool dispatcher. They prove the one
// thing unit tests can't: that the per-request TokenInfo actually reaches the
// tool handler (via RequestExtra) so authorize()/RBAC fires end-to-end. They use
// only store-backed tools (list_hosts, recent_audit) and the pre-Docker denial
// path of start_container, so no Docker daemon is required.

// bearerRT injects a fixed bearer token on every outbound request.
type bearerRT struct {
	token string
	base  http.RoundTripper
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

// smokeCheckAccess mirrors the production per-user gate (admin bypass, section
// membership, read-only on writes). It intentionally omits the global
// DisabledSections admin toggle (orthogonal to what these tests exercise); the
// point here is to drive the live-RBAC leg of authorize() end-to-end.
func smokeCheckAccess(_ context.Context, u *store.User, section string, write bool) error {
	if u.IsAdmin() {
		return nil
	}
	if !contains(u.Sections, section) {
		return errors.New("section not permitted")
	}
	if u.ReadOnly && write {
		return errors.New("account is read-only")
	}
	return nil
}

func newSmokeServer(t *testing.T) (*httptest.Server, *store.Store, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateHost(context.Background(), &store.Host{Name: "local", Kind: "local"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	uid, err := st.CreateUser(context.Background(), &store.User{
		Username: "smoke", PasswordHash: "x", Role: "user", Sections: []string{"hosts", "containers"},
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	// Static OAuth URLs so the 401 carries a WWW-Authenticate discovery header.
	// (Values are advertised only; SigningKey is nil so the JWT path stays off
	// and opaque API tokens — which contain no dots — are used throughout.)
	deps := Deps{
		Store: st, CheckAccess: smokeCheckAccess, Version: "test",
		ResourceURL: "https://smoke.test/mcp",
		MetadataURL: "https://smoke.test/.well-known/oauth-protected-resource",
		IssuerURL:   "https://smoke.test",
	}
	mcpHandler, _ := deps.Handlers()
	ts := httptest.NewServer(mcpHandler)
	t.Cleanup(ts.Close)
	return ts, st, uid
}

func mkToken(t *testing.T, st *store.Store, uid int64, secret string, sections []string, ro bool) {
	t.Helper()
	sum := sha256.Sum256([]byte(secret))
	if _, err := st.CreateAPIToken(context.Background(), &store.APIToken{
		UserID: uid, TokenHash: hex.EncodeToString(sum[:]), Name: secret, Sections: sections, ReadOnly: ro,
	}); err != nil {
		t.Fatalf("token: %v", err)
	}
}

func connect(t *testing.T, url, token string) (*mcpsdk.ClientSession, context.Context) {
	t.Helper()
	hc := &http.Client{Transport: bearerRT{token: token, base: http.DefaultTransport}}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "smoke", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: url, HTTPClient: hc, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ctx
}

// 1. No bearer token → the transport is rejected with 401 before any MCP work.
func TestSmoke_NoTokenRejected(t *testing.T) {
	ts, _, _ := newSmokeServer(t)
	resp, err := http.Post(ts.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 without bearer, got %d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header on 401")
	}
}

//  2. The full end-to-end path: a valid token lists tools and calls a read tool,
//     proving TokenInfo reaches the handler and the dispatch succeeds.
func TestSmoke_EndToEndReadTool(t *testing.T) {
	ts, st, uid := newSmokeServer(t)
	mkToken(t, st, uid, "full-token-secret", nil, false)
	cs, ctx := connect(t, ts.URL, "full-token-secret")

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) == 0 || !hasTool(tools.Tools, "list_hosts") {
		t.Fatalf("expected list_hosts among %d tools", len(tools.Tools))
	}

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_hosts", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call list_hosts: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_hosts returned tool error: %+v", res.Content)
	}
	// Assert on the actual payload (the store was seeded with one host "local"),
	// so the test can't pass on an empty/garbage result.
	body, _ := json.Marshal(res.StructuredContent)
	if !bytes.Contains(body, []byte(`"local"`)) {
		t.Fatalf("expected seeded host \"local\" in list_hosts output: %s", body)
	}
}

//  5. The DECISIVE test for the user-RBAC leg: an UN-narrowed token (no section
//     subset, not read-only) calling a tool whose section the USER lacks. Token
//     narrowing can't deny here (no scopes), so the only thing that can produce
//     the error is the live CheckAccess() leg of authorize() — proving it fires
//     end-to-end. Without this, removing the CheckAccess call would go unnoticed.
func TestSmoke_UserRBACLegEnforced(t *testing.T) {
	ts, st, uid := newSmokeServer(t) // seeded user has hosts+containers, NOT audit
	mkToken(t, st, uid, "unnarrowed-secret", nil, false)
	cs, ctx := connect(t, ts.URL, "unnarrowed-secret")

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "recent_audit", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("un-narrowed token must still be denied recent_audit: the user lacks the 'audit' section — proves the live CheckAccess RBAC leg fires, not just token narrowing")
	}
}

//  3. A token scoped to a different section is denied at the tool — proving token
//     narrowing is enforced end-to-end (not just in unit tests).
func TestSmoke_TokenSectionNarrowingEnforced(t *testing.T) {
	ts, st, uid := newSmokeServer(t)
	mkToken(t, st, uid, "scoped-token-secret", []string{"containers"}, false)
	cs, ctx := connect(t, ts.URL, "scoped-token-secret")

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_hosts", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("token scoped to 'containers' must be denied list_hosts (section 'hosts')")
	}
}

//  4. A read-only token is denied a write tool — and the denial happens in
//     authorize(), before any Docker call, so no daemon is needed.
func TestSmoke_ReadOnlyTokenBlocksWrite(t *testing.T) {
	ts, st, uid := newSmokeServer(t)
	mkToken(t, st, uid, "ro-token-secret", nil, true)
	cs, ctx := connect(t, ts.URL, "ro-token-secret")

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "start_container", Arguments: map[string]any{"container_id": "anything"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("read-only token must be denied start_container")
	}
}

func hasTool(tools []*mcpsdk.Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
