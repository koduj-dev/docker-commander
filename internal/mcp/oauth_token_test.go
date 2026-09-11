package mcp

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

const (
	testIssuer   = "https://dc.example.com"
	testResource = "https://dc.example.com/mcp"
)

func TestAccessTokenRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	tok, exp, err := MintAccessToken(key, testIssuer, testResource, 42, "cli-test", "sess-test", true, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if exp.Before(time.Now()) {
		t.Fatal("expiry should be in the future")
	}

	uid, gotCID, gotSID, ro, gotExp, err := parseAccessToken(key, testResource, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if uid != 42 || !ro {
		t.Fatalf("round-trip mismatch: uid=%d ro=%v", uid, ro)
	}
	// The client binding has to survive the round trip, or revocation-by-removing
	// -the-client silently degrades to "not revocable".
	if gotCID != "cli-test" {
		t.Fatalf("client id did not survive the round trip: %q", gotCID)
	}
	// Likewise the session binding — it is what lets ONE session be revoked
	// without removing the whole client.
	if gotSID != "sess-test" {
		t.Fatalf("session id did not survive the round trip: %q", gotSID)
	}
	if gotExp.Unix() != exp.Unix() {
		t.Fatalf("expiry mismatch: %v vs %v", gotExp, exp)
	}
}

func TestAccessTokenRejections(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("ffffffffffffffffffffffffffffffff")

	tok, _, _ := MintAccessToken(key, testIssuer, testResource, 7, "cli-test", "sess-test", false, time.Hour)

	t.Run("wrong audience rejected (RFC 8707 binding)", func(t *testing.T) {
		if _, _, _, _, _, err := parseAccessToken(key, "https://evil.example.com/mcp", tok); err == nil {
			t.Fatal("token for a different resource must be rejected")
		}
	})
	t.Run("wrong signing key rejected", func(t *testing.T) {
		if _, _, _, _, _, err := parseAccessToken(other, testResource, tok); err == nil {
			t.Fatal("token signed with another key must be rejected")
		}
	})
	t.Run("expired token rejected", func(t *testing.T) {
		expired, _, _ := MintAccessToken(key, testIssuer, testResource, 7, "cli-test", "sess-test", false, -time.Minute)
		if _, _, _, _, _, err := parseAccessToken(key, testResource, expired); err == nil {
			t.Fatal("expired token must be rejected")
		}
	})
	t.Run("garbage rejected", func(t *testing.T) {
		if _, _, _, _, _, err := parseAccessToken(key, testResource, "not.a.jwt"); err == nil {
			t.Fatal("malformed token must be rejected")
		}
	})
}

// TestVerifyTokenOAuthPath confirms verifyToken accepts an OAuth-minted JWT and
// produces a principal honoring the token's read-only grant, alongside the
// opaque API-token path.
func TestVerifyTokenOAuthPath(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uid, err := st.CreateUser(context.Background(), &store.User{Username: "bob", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	// The token is bound to a client, and verification requires that client to
	// still be registered — so the fixture has to register it.
	if err := st.CreateOAuthClient(context.Background(), &store.OAuthClient{
		ID: "cli-test", Name: "test connector", RedirectURIs: []string{"http://127.0.0.1/cb"},
	}); err != nil {
		t.Fatalf("client: %v", err)
	}
	// Likewise the session: verification requires the session row to still
	// exist, same as the client above.
	if err := st.CreateMCPOAuthSession(context.Background(), &store.MCPOAuthSession{
		ID: "sess-test", ClientID: "cli-test", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	h := newHandler(Deps{
		Store: st, SigningKey: key, ResourceURL: testResource, IssuerURL: testIssuer,
		CheckAccess: func(context.Context, *store.User, string, bool, int64) error { return nil },
	})

	tok, _, _ := MintAccessToken(key, testIssuer, testResource, uid, "cli-test", "sess-test", true, time.Hour)
	ti, err := h.verifyToken(context.Background(), tok, httptest.NewRequest("POST", "/mcp", nil))
	if err != nil {
		t.Fatalf("verify oauth token: %v", err)
	}
	p, _ := ti.Extra[principalKey].(*principal)
	if p == nil || p.user.ID != uid || !p.roOnly {
		t.Fatalf("oauth principal wrong: %+v", p)
	}

	// A bogus JWT-shaped token must be rejected, not silently accepted.
	if _, err := h.verifyToken(context.Background(), "aaa.bbb.ccc", httptest.NewRequest("POST", "/mcp", nil)); err == nil {
		t.Fatal("bogus JWT-shaped token should be rejected")
	}

	// A token minted before dc_cid existed carries no client. It must still
	// verify: rejecting it would force every connector to re-authorize on upgrade
	// to buy at most one token lifetime.
	//
	// It is forged with the raw JWT library on purpose — MintAccessToken now
	// refuses an empty client, so the only honest way to produce the old shape is
	// to build it the way the old code did.
	legacy := legacyAccessToken(t, key, uid)
	if _, err := h.verifyToken(context.Background(), legacy, httptest.NewRequest("POST", "/mcp", nil)); err != nil {
		t.Fatalf("a token without a client binding should still verify: %v", err)
	}
}

// legacyAccessToken builds an access token in the pre-dc_cid shape.
func legacyAccessToken(t *testing.T, key []byte, uid int64) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		ReadOnly: true,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Subject:   strconv.FormatInt(uid, 10),
			Audience:  jwt.ClaimStrings{testResource},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			ID:        "legacy",
		},
	})
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestMintAccessTokenRefusesAnUnboundToken pins the decision that a token with no
// client, or no session, can never be *minted*, only tolerated on parse.
// Without this, a future caller passing "" would silently reintroduce an
// unrevocable token and every existing test would still pass.
func TestMintAccessTokenRefusesAnUnboundToken(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	if _, _, err := MintAccessToken(key, testIssuer, testResource, 1, "", "sess-test", false, time.Hour); err == nil {
		t.Fatal("minting an access token with no client id must fail — such a token could never be revoked")
	}
	if _, _, err := MintAccessToken(key, testIssuer, testResource, 1, "cli-test", "", false, time.Hour); err == nil {
		t.Fatal("minting an access token with no session id must fail — its session could never be revoked")
	}
}

// TestPentestRemovingAnOAuthClientRevokesItsAccessTokens is the point of the
// client binding.
//
// An access token is a signed bearer credential: nothing about removing a
// connector reaches the copy a tool already holds, so before this the admin
// action purged codes and refresh tokens while the tool kept working until the
// token expired — up to AccessTokenTTL of access after "revoke". The window was
// bounded, which is why this is hardening rather than a hole, but "revoked" has
// to mean now.
func TestPentestRemovingAnOAuthClientRevokesItsAccessTokens(t *testing.T) {
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	uid, err := st.CreateUser(ctx, &store.User{Username: "mallory", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := st.CreateOAuthClient(ctx, &store.OAuthClient{
		ID: "cli-doomed", Name: "connector", RedirectURIs: []string{"http://127.0.0.1/cb"},
	}); err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := st.CreateMCPOAuthSession(ctx, &store.MCPOAuthSession{
		ID: "sess-doomed", ClientID: "cli-doomed", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	h := newHandler(Deps{
		Store: st, SigningKey: key, ResourceURL: testResource, IssuerURL: testIssuer,
		CheckAccess: func(context.Context, *store.User, string, bool, int64) error { return nil },
	})

	// A long-lived token so the test can only pass because of revocation, never
	// because the token happened to expire.
	tok, _, _ := MintAccessToken(key, testIssuer, testResource, uid, "cli-doomed", "sess-doomed", false, time.Hour)
	if _, err := h.verifyToken(ctx, tok, httptest.NewRequest("POST", "/mcp", nil)); err != nil {
		t.Fatalf("token should work while its client is registered: %v", err)
	}

	if ok, err := st.DeleteOAuthClient(ctx, "cli-doomed"); err != nil || !ok {
		t.Fatalf("delete client: ok=%v err=%v", ok, err)
	}

	if _, err := h.verifyToken(ctx, tok, httptest.NewRequest("POST", "/mcp", nil)); err == nil {
		t.Fatal("SECURITY: an access token still works after its OAuth client was removed — revoking a connector does not revoke its in-flight access")
	}
}

// TestPentestRevokedSessionAccessTokenRejectedImmediately is the point of
// per-SESSION revocation: unlike TestPentestRemovingAnOAuthClientRevokesItsAccessTokens
// above, this revokes only ONE session while its client (and any of the
// client's other sessions) stays registered — the narrower operation the
// coarser client-removal check above cannot express.
func TestPentestRevokedSessionAccessTokenRejectedImmediately(t *testing.T) {
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	uid, err := st.CreateUser(ctx, &store.User{Username: "carol", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := st.CreateOAuthClient(ctx, &store.OAuthClient{
		ID: "cli-multi", Name: "connector", RedirectURIs: []string{"http://127.0.0.1/cb"},
	}); err != nil {
		t.Fatalf("client: %v", err)
	}
	// Two independent sessions under the SAME client — e.g. the same tool paired
	// from two machines. Revoking one must not touch the other.
	for _, sid := range []string{"sess-a", "sess-b"} {
		if err := st.CreateMCPOAuthSession(ctx, &store.MCPOAuthSession{
			ID: sid, ClientID: "cli-multi", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("session %s: %v", sid, err)
		}
	}
	h := newHandler(Deps{
		Store: st, SigningKey: key, ResourceURL: testResource, IssuerURL: testIssuer,
		CheckAccess: func(context.Context, *store.User, string, bool, int64) error { return nil },
	})

	// Long-lived tokens so the test can only pass because of revocation, never
	// because a token happened to expire.
	tokA, _, _ := MintAccessToken(key, testIssuer, testResource, uid, "cli-multi", "sess-a", false, time.Hour)
	tokB, _, _ := MintAccessToken(key, testIssuer, testResource, uid, "cli-multi", "sess-b", false, time.Hour)

	if err := st.RevokeMCPOAuthSession(ctx, "sess-a", uid); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := h.verifyToken(ctx, tokA, httptest.NewRequest("POST", "/mcp", nil)); err == nil {
		t.Fatal("SECURITY: a still-time-valid access token works after its own session was revoked")
	}
	if _, err := h.verifyToken(ctx, tokB, httptest.NewRequest("POST", "/mcp", nil)); err != nil {
		t.Fatalf("a sibling session under the same client must be unaffected: %v", err)
	}
}
