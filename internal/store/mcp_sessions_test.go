package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mcpSessionStore(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	uid, err := st.CreateUser(context.Background(), &User{Username: "alice", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOAuthClient(context.Background(), &OAuthClient{
		ID: "cli-1", Name: "connector", RedirectURIs: []string{"http://127.0.0.1/cb"},
	}); err != nil {
		t.Fatal(err)
	}
	return st, uid
}

func TestMCPOAuthSessionsExpiryIsInvisibleAndPurgeable(t *testing.T) {
	st, uid := mcpSessionStore(t)
	ctx := context.Background()

	live := &MCPOAuthSession{ID: "live", ClientID: "cli-1", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}
	dead := &MCPOAuthSession{ID: "dead", ClientID: "cli-1", UserID: uid, ExpiresAt: time.Now().Add(-time.Hour)}
	for _, s := range []*MCPOAuthSession{live, dead} {
		if err := st.CreateMCPOAuthSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.ListMCPOAuthSessions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "live" {
		t.Fatalf("expired session should not be listed, got %+v", got)
	}

	if err := st.DeleteExpiredOAuth(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.MCPOAuthSessionExists(ctx, "dead", uid); ok {
		t.Error("sweep left an expired row behind")
	}
	if ok, _ := st.MCPOAuthSessionExists(ctx, "live", uid); !ok {
		t.Error("sweep took a live session with it")
	}
}

func TestListAllMCPOAuthSessionsSeesEveryUser(t *testing.T) {
	st, uid1 := mcpSessionStore(t)
	ctx := context.Background()
	uid2, err := st.CreateUser(ctx, &User{Username: "bob", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}

	for i, uid := range []int64{uid1, uid2} {
		if err := st.CreateMCPOAuthSession(ctx, &MCPOAuthSession{
			ID: "sess-" + string(rune('a'+i)), ClientID: "cli-1", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	own, err := st.ListMCPOAuthSessions(ctx, uid1)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 {
		t.Fatalf("own list should see only the caller's session, got %d", len(own))
	}

	all, err := st.ListAllMCPOAuthSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("admin list should see every user's session, got %d", len(all))
	}
}

// TestRevokeMCPOAuthSessionAlsoKillsTheRefreshToken is the point of the
// feature: deleting only the session row would leave a still-valid refresh
// token able to mint a fresh access token, silently resurrecting a "revoked"
// session. Both rows must go together, in one transaction.
func TestRevokeMCPOAuthSessionAlsoKillsTheRefreshToken(t *testing.T) {
	st, uid := mcpSessionStore(t)
	ctx := context.Background()

	if err := st.CreateMCPOAuthSession(ctx, &MCPOAuthSession{
		ID: "sess-1", ClientID: "cli-1", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRefreshToken(ctx, "hash-1", &OAuthRefreshToken{
		ClientID: "cli-1", UserID: uid, SessionID: "sess-1", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.RevokeMCPOAuthSession(ctx, "sess-1", uid); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if ok, _ := st.MCPOAuthSessionExists(ctx, "sess-1", uid); ok {
		t.Error("SECURITY: session row survived revocation")
	}
	if _, err := st.ConsumeRefreshToken(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("SECURITY: the session's refresh token still works after revoke — the session can resurrect itself on its next refresh")
	}
}

// TestRevokeMCPOAuthSessionIsScopedToTheOwner is the IDOR guard: knowing (or
// guessing) another user's session id must revoke nothing.
func TestRevokeMCPOAuthSessionIsScopedToTheOwner(t *testing.T) {
	st, uid1 := mcpSessionStore(t)
	ctx := context.Background()
	uid2, err := st.CreateUser(ctx, &User{Username: "bob", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateMCPOAuthSession(ctx, &MCPOAuthSession{
		ID: "sess-victim", ClientID: "cli-1", UserID: uid1, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.RevokeMCPOAuthSession(ctx, "sess-victim", uid2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking another user's session must fail as not-found, got %v", err)
	}
	if ok, _ := st.MCPOAuthSessionExists(ctx, "sess-victim", uid1); !ok {
		t.Error("SECURITY: a session was revoked by a user who does not own it")
	}
}

// TestAdminRevokeMCPOAuthSessionIsUnscoped mirrors AdminRevokeAPIToken /
// DeleteOAuthClient: an admin can revoke ANY user's session, unlike the
// self-service path above.
func TestAdminRevokeMCPOAuthSessionIsUnscoped(t *testing.T) {
	st, uid := mcpSessionStore(t)
	ctx := context.Background()
	if err := st.CreateMCPOAuthSession(ctx, &MCPOAuthSession{
		ID: "sess-1", ClientID: "cli-1", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AdminRevokeMCPOAuthSession(ctx, "sess-1"); err != nil {
		t.Fatalf("admin revoke: %v", err)
	}
	if ok, _ := st.MCPOAuthSessionExists(ctx, "sess-1", uid); ok {
		t.Error("admin revoke left the session in place")
	}
}

// TestDeleteOAuthClientPurgesItsMCPSessions ensures removing a client's
// registration doesn't leave orphaned session rows behind — the same
// comprehensiveness DeleteOAuthClient already gives codes/refresh tokens.
func TestDeleteOAuthClientPurgesItsMCPSessions(t *testing.T) {
	st, uid := mcpSessionStore(t)
	ctx := context.Background()
	if err := st.CreateMCPOAuthSession(ctx, &MCPOAuthSession{
		ID: "sess-1", ClientID: "cli-1", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.DeleteOAuthClient(ctx, "cli-1"); err != nil || !ok {
		t.Fatalf("delete client: ok=%v err=%v", ok, err)
	}
	if ok, _ := st.MCPOAuthSessionExists(ctx, "sess-1", uid); ok {
		t.Error("deleting a client left one of its sessions behind")
	}
}
