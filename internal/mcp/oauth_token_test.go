package mcp

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

const (
	testIssuer   = "https://dc.example.com"
	testResource = "https://dc.example.com/mcp"
)

func TestAccessTokenRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	tok, exp, err := MintAccessToken(key, testIssuer, testResource, 42, true, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if exp.Before(time.Now()) {
		t.Fatal("expiry should be in the future")
	}

	uid, ro, gotExp, err := parseAccessToken(key, testResource, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if uid != 42 || !ro {
		t.Fatalf("round-trip mismatch: uid=%d ro=%v", uid, ro)
	}
	if gotExp.Unix() != exp.Unix() {
		t.Fatalf("expiry mismatch: %v vs %v", gotExp, exp)
	}
}

func TestAccessTokenRejections(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("ffffffffffffffffffffffffffffffff")

	tok, _, _ := MintAccessToken(key, testIssuer, testResource, 7, false, time.Hour)

	t.Run("wrong audience rejected (RFC 8707 binding)", func(t *testing.T) {
		if _, _, _, err := parseAccessToken(key, "https://evil.example.com/mcp", tok); err == nil {
			t.Fatal("token for a different resource must be rejected")
		}
	})
	t.Run("wrong signing key rejected", func(t *testing.T) {
		if _, _, _, err := parseAccessToken(other, testResource, tok); err == nil {
			t.Fatal("token signed with another key must be rejected")
		}
	})
	t.Run("expired token rejected", func(t *testing.T) {
		expired, _, _ := MintAccessToken(key, testIssuer, testResource, 7, false, -time.Minute)
		if _, _, _, err := parseAccessToken(key, testResource, expired); err == nil {
			t.Fatal("expired token must be rejected")
		}
	})
	t.Run("garbage rejected", func(t *testing.T) {
		if _, _, _, err := parseAccessToken(key, testResource, "not.a.jwt"); err == nil {
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
	h := &handler{deps: Deps{
		Store: st, SigningKey: key, ResourceURL: testResource, IssuerURL: testIssuer,
		CheckAccess: func(context.Context, *store.User, string, bool) error { return nil },
	}}

	tok, _, _ := MintAccessToken(key, testIssuer, testResource, uid, true, time.Hour)
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
}
