package store

import (
	"testing"
	"time"
)

// TestListAllAPITokens covers the admin overview query: every user's tokens,
// annotated with the owner's username, newest-first, including the revoked flag.
func TestListAllAPITokens(t *testing.T) {
	s, ctx := newStore(t)

	alice, err := s.CreateUser(ctx, &User{Username: "alice", PasswordHash: "h", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, &User{Username: "bob", PasswordHash: "h", Role: "user", Sections: []string{"containers"}})
	if err != nil {
		t.Fatal(err)
	}

	// Empty to start.
	if all, err := s.ListAllAPITokens(ctx); err != nil || len(all) != 0 {
		t.Fatalf("fresh store: %v len=%d", err, len(all))
	}

	a1, _ := s.CreateAPIToken(ctx, &APIToken{UserID: alice, TokenHash: "ha1", Name: "alice-1"})
	b1, _ := s.CreateAPIToken(ctx, &APIToken{UserID: bob, TokenHash: "hb1", Name: "bob-1", ReadOnly: true, Sections: []string{"containers"}})

	all, err := s.ListAllAPITokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 tokens, got %d", len(all))
	}
	// Newest-first: bob's token (higher id) comes first.
	if all[0].ID != b1 || all[0].Username != "bob" || !all[0].ReadOnly {
		t.Errorf("first row wrong: %+v", all[0])
	}
	if all[1].ID != a1 || all[1].Username != "alice" {
		t.Errorf("second row wrong: %+v", all[1])
	}
	if len(all[0].Sections) != 1 || all[0].Sections[0] != "containers" {
		t.Errorf("sections not round-tripped: %+v", all[0].Sections)
	}

	// Revoking surfaces via the flag (the row is still returned).
	if ok, err := s.AdminRevokeAPIToken(ctx, b1); err != nil || !ok {
		t.Fatalf("AdminRevokeAPIToken: ok=%v err=%v", ok, err)
	}
	all, _ = s.ListAllAPITokens(ctx)
	if len(all) != 2 || !all[0].Revoked {
		t.Errorf("revoked flag not surfaced: %+v", all)
	}
}

// TestAdminRevokeAPIToken is not scoped to an owner and reports whether a still
// active token was revoked.
func TestAdminRevokeAPIToken(t *testing.T) {
	s, ctx := newStore(t)
	uid, _ := s.CreateUser(ctx, &User{Username: "u", PasswordHash: "h", Role: "user"})
	id, _ := s.CreateAPIToken(ctx, &APIToken{UserID: uid, TokenHash: "h1", Name: "t"})

	// Unknown id → false, no error.
	if ok, err := s.AdminRevokeAPIToken(ctx, 99999); err != nil || ok {
		t.Errorf("unknown id: ok=%v err=%v", ok, err)
	}
	// First revoke succeeds; the token can no longer be looked up by hash.
	if ok, err := s.AdminRevokeAPIToken(ctx, id); err != nil || !ok {
		t.Errorf("first revoke: ok=%v err=%v", ok, err)
	}
	if _, err := s.APITokenByHash(ctx, "h1"); err != ErrNotFound {
		t.Errorf("revoked token should not resolve by hash: %v", err)
	}
	// Second revoke is a no-op → false (already revoked).
	if ok, _ := s.AdminRevokeAPIToken(ctx, id); ok {
		t.Error("double revoke should report false")
	}
}

// TestListAndDeleteOAuthClients covers the admin OAuth client overview and the
// cascading delete (codes + refresh tokens issued to the client go too).
func TestListAndDeleteOAuthClients(t *testing.T) {
	s, ctx := newStore(t)

	if cs, err := s.ListOAuthClients(ctx); err != nil || len(cs) != 0 {
		t.Fatalf("fresh: %v len=%d", err, len(cs))
	}

	c := &OAuthClient{ID: "dcmcp_one", Name: "Client One", RedirectURIs: []string{"https://a/cb", "https://b/cb"}}
	if err := s.CreateOAuthClient(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Issue a code and a refresh token bound to the client.
	exp := time.Now().Add(time.Hour)
	if err := s.CreateOAuthCode(ctx, "codehash", &OAuthCode{ClientID: c.ID, UserID: 1, RedirectURI: "https://a/cb", ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRefreshToken(ctx, "rthash", &OAuthRefreshToken{ClientID: c.ID, UserID: 1, ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}

	clients, err := s.ListOAuthClients(ctx)
	if err != nil || len(clients) != 1 {
		t.Fatalf("list: %v len=%d", err, len(clients))
	}
	if clients[0].ID != c.ID || clients[0].Name != "Client One" || len(clients[0].RedirectURIs) != 2 {
		t.Errorf("client round-trip wrong: %+v", clients[0])
	}

	// Delete cascades: the code and refresh token must be gone too.
	if ok, err := s.DeleteOAuthClient(ctx, c.ID); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if _, err := s.OAuthClientByID(ctx, c.ID); err != ErrNotFound {
		t.Errorf("client should be gone: %v", err)
	}
	if _, err := s.ConsumeOAuthCode(ctx, "codehash"); err != ErrNotFound {
		t.Errorf("code should be purged with the client: %v", err)
	}
	if _, err := s.ConsumeRefreshToken(ctx, "rthash"); err != ErrNotFound {
		t.Errorf("refresh token should be purged with the client: %v", err)
	}

	// Deleting an unknown client → false, no error.
	if ok, err := s.DeleteOAuthClient(ctx, "nope"); err != nil || ok {
		t.Errorf("unknown client: ok=%v err=%v", ok, err)
	}
}
