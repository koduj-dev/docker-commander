package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The parts of the session table the HTTP tests cannot reach: what expiry does
// to a row, how much of a client's own text we agree to keep, and whether a
// deleted account takes its sessions with it.

func sessionStore(t *testing.T) (*Store, int64) {
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
	return st, uid
}

func TestSessionsExpiryIsInvisibleAndPurgeable(t *testing.T) {
	st, uid := sessionStore(t)
	ctx := context.Background()

	live := &Session{ID: "live", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}
	dead := &Session{ID: "dead", UserID: uid, ExpiresAt: time.Now().Add(-time.Hour)}
	for _, s := range []*Session{live, dead} {
		if err := st.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	// An expired token is refused anyway; offering it for revocation would be
	// asking the user to act on something already gone.
	got, err := st.ListSessions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "live" {
		t.Fatalf("expired session should not be listed, got %+v", got)
	}

	if err := st.PurgeExpiredSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.SessionExists(ctx, "dead", uid); ok {
		t.Error("purge left an expired row behind")
	}
	if ok, _ := st.SessionExists(ctx, "live", uid); !ok {
		t.Error("purge took a live session with it")
	}
}

// SessionExists is the middleware's gate, so it must be scoped: a valid session
// id belonging to someone else must not answer yes.
func TestSessionExistsIsScopedToItsOwner(t *testing.T) {
	st, uid := sessionStore(t)
	ctx := context.Background()
	other, err := st.CreateUser(ctx, &User{Username: "mallory", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, &Session{ID: "alice-1", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if ok, err := st.SessionExists(ctx, "alice-1", other); ok || err != nil {
		t.Errorf("SECURITY: another account's session id validated (%v, %v)", ok, err)
	}
	if ok, err := st.SessionExists(ctx, "alice-1", uid); !ok || err != nil {
		t.Errorf("the owner's own session should validate (%v, %v)", ok, err)
	}
}

// The user agent is text the client chooses, stored and later rendered back to
// the account owner. Keep it to what identifies a browser.
func TestSessionUserAgentIsBounded(t *testing.T) {
	st, uid := sessionStore(t)
	ctx := context.Background()
	if err := st.CreateSession(ctx, &Session{
		ID: "s", UserID: uid, UserAgent: strings.Repeat("A", 4096),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListSessions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].UserAgent) != 256 {
		t.Errorf("user agent stored at %d chars, want it capped at 256", len(got[0].UserAgent))
	}
}

// Deleting an account must not leave rows that a recycled user id would inherit.
func TestDeleteUserTakesItsSessions(t *testing.T) {
	st, uid := sessionStore(t)
	ctx := context.Background()
	if err := st.CreateSession(ctx, &Session{ID: "s", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.SessionExists(ctx, "s", uid); ok {
		t.Error("a deleted account kept its session row")
	}
}

// TouchSession is deliberately lazy — it runs on every authenticated request, and
// writing every time would turn a read-mostly workload into a write-mostly one
// against a single-writer database.
func TestTouchSessionOnlyWritesOnceAMinute(t *testing.T) {
	st, uid := sessionStore(t)
	ctx := context.Background()
	if err := st.CreateSession(ctx, &Session{ID: "s", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// Ten seconds old: recent enough that a request should leave it alone. It has
	// to be backdated, or "wrote" and "did not write" both produce the same
	// second-granularity timestamp and the assertion proves nothing.
	recent := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = 's'`, recent); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListSessions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].LastSeenAt.Format(time.RFC3339) != recent {
		t.Errorf("a recently used session should not be rewritten on every request (was %s, now %s)",
			recent, after[0].LastSeenAt.Format(time.RFC3339))
	}

	// Backdate it and the write happens — otherwise "last used" would freeze at
	// the login time and tell the user nothing.
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = 's'`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.ListSessions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(fresh[0].LastSeenAt) > time.Minute {
		t.Errorf("a stale session was not touched: last seen %v", fresh[0].LastSeenAt)
	}
}
