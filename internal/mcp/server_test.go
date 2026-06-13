package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// newTestHandler spins up an in-memory store with one user and returns a handler
// plus the user ID. CheckAccess defaults to permissive so tests can isolate the
// token-narrowing layer; pass a custom one to test RBAC propagation.
func newTestHandler(t *testing.T, check CheckAccessFunc) (*handler, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uid, err := st.CreateUser(context.Background(), &store.User{Username: "alice", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if check == nil {
		check = func(context.Context, *store.User, string, bool) error { return nil }
	}
	return &handler{deps: Deps{Store: st, CheckAccess: check}}, uid
}

func TestVerifyToken(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()

	mk := func(secret string, ro bool, sections []string, exp time.Time) {
		if _, err := h.deps.Store.CreateAPIToken(ctx, &store.APIToken{
			UserID: uid, TokenHash: hashToken(secret), Name: secret,
			ReadOnly: ro, Sections: sections, ExpiresAt: exp,
		}); err != nil {
			t.Fatalf("create token %q: %v", secret, err)
		}
	}
	mk("good", true, []string{"containers"}, time.Time{})
	mk("expired", false, nil, time.Now().Add(-time.Hour))

	t.Run("valid token yields narrowed principal", func(t *testing.T) {
		ti, err := h.verifyToken(ctx, "good", httptest.NewRequest("POST", "/mcp", nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		p, _ := ti.Extra[principalKey].(*principal)
		if p == nil || p.user.ID != uid {
			t.Fatalf("principal not populated: %+v", ti.Extra)
		}
		if !p.roOnly || len(p.scopes) != 1 || p.scopes[0] != "containers" {
			t.Fatalf("token narrowing not carried: roOnly=%v scopes=%v", p.roOnly, p.scopes)
		}
		if ti.Expiration.IsZero() {
			t.Fatal("never-expiring token must report a non-zero Expiration to the SDK")
		}
	})

	t.Run("unknown token rejected", func(t *testing.T) {
		if _, err := h.verifyToken(ctx, "nope", httptest.NewRequest("POST", "/mcp", nil)); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("want ErrInvalidToken, got %v", err)
		}
	})

	t.Run("expired token rejected", func(t *testing.T) {
		if _, err := h.verifyToken(ctx, "expired", httptest.NewRequest("POST", "/mcp", nil)); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("want ErrInvalidToken, got %v", err)
		}
	})

	t.Run("revoked token rejected", func(t *testing.T) {
		// Revoke "good" and confirm it stops verifying.
		toks, _ := h.deps.Store.ListAPITokens(ctx, uid)
		var id int64
		for _, tk := range toks {
			if tk.Name == "good" {
				id = tk.ID
			}
		}
		if err := h.deps.Store.RevokeAPIToken(ctx, id, uid); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if _, err := h.verifyToken(ctx, "good", httptest.NewRequest("POST", "/mcp", nil)); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("revoked token still verifies: %v", err)
		}
	})
}

func TestPrincipalNarrowed(t *testing.T) {
	cases := []struct {
		name    string
		p       principal
		section string
		write   bool
		wantErr bool
	}{
		{"read-only token blocks write", principal{roOnly: true}, "containers", true, true},
		{"read-only token allows read", principal{roOnly: true}, "containers", false, false},
		{"scoped token blocks other section", principal{scopes: []string{"containers"}}, "images", false, true},
		{"scoped token allows in-scope", principal{scopes: []string{"containers"}}, "containers", false, false},
		{"unscoped token allows any section", principal{}, "images", false, false},
		{"scoped write within scope ok (narrowing only)", principal{scopes: []string{"projects"}}, "projects", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.narrowed(tc.section, tc.write)
			if (err != nil) != tc.wantErr {
				t.Fatalf("narrowed(%q, write=%v) err=%v, wantErr=%v", tc.section, tc.write, err, tc.wantErr)
			}
		})
	}
}

// reqFor builds a CallToolRequest carrying the principal exactly as the
// streamable transport delivers it — via Extra.TokenInfo, not the context.
// This exercises the real authorize() extraction path.
func reqFor(p *principal) *mcpsdk.CallToolRequest {
	return &mcpsdk.CallToolRequest{
		Extra: &mcpsdk.RequestExtra{
			TokenInfo: &auth.TokenInfo{Extra: map[string]any{principalKey: p}},
		},
	}
}

func TestAuthorize(t *testing.T) {
	ctx := context.Background()

	t.Run("missing principal is unauthenticated", func(t *testing.T) {
		h, _ := newTestHandler(t, nil)
		if _, err := h.authorize(ctx, &mcpsdk.CallToolRequest{}, "containers", false); err == nil {
			t.Fatal("expected unauthenticated error with no TokenInfo")
		}
	})

	t.Run("token narrowing applies before user RBAC", func(t *testing.T) {
		// Permissive RBAC, but a read-only token must still block a write.
		h, uid := newTestHandler(t, nil)
		u, _ := h.deps.Store.UserByID(ctx, uid)
		req := reqFor(&principal{user: u, roOnly: true})
		if _, err := h.authorize(ctx, req, "containers", true); err == nil {
			t.Fatal("read-only token should block a write even with permissive RBAC")
		}
		if _, err := h.authorize(ctx, req, "containers", false); err != nil {
			t.Fatalf("read-only token should still allow reads: %v", err)
		}
	})

	t.Run("live RBAC denial is the final word", func(t *testing.T) {
		denied := errors.New("access to this section is not permitted")
		h, uid := newTestHandler(t, func(context.Context, *store.User, string, bool) error { return denied })
		u, _ := h.deps.Store.UserByID(ctx, uid)
		req := reqFor(&principal{user: u}) // token grants everything…
		if _, err := h.authorize(ctx, req, "containers", false); !errors.Is(err, denied) {
			t.Fatalf("RBAC denial not surfaced through authorize: %v", err)
		}
	})

	t.Run("allowed when token and RBAC both pass", func(t *testing.T) {
		h, uid := newTestHandler(t, nil)
		u, _ := h.deps.Store.UserByID(ctx, uid)
		req := reqFor(&principal{user: u, scopes: []string{"containers"}})
		if _, err := h.authorize(ctx, req, "containers", false); err != nil {
			t.Fatalf("expected allow: %v", err)
		}
	})
}
