package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func factorStore(t *testing.T) (*Store, int64) {
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

// The upgrade path. An installation that predates multiple authenticators holds
// the secret on the user row, and that user must keep signing in with the phone
// already in their hand — a migration that quietly locks people out is worse than
// no feature.
func TestMigrateTOTPToFactorsMovesTheSecretAndClearsIt(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()

	// Write the old shape directly: the current code cannot produce it any more.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = 'LEGACYSECRET', totp_enabled = 1, totp_last_counter = 42 WHERE id = ?`,
		uid); err != nil {
		t.Fatal(err)
	}

	if err := st.migrateTOTPToFactors(ctx); err != nil {
		t.Fatal(err)
	}

	factors, err := st.ListFactors(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(factors) != 1 {
		t.Fatalf("want the old authenticator migrated to 1 factor, got %d", len(factors))
	}
	if factors[0].Secret != "LEGACYSECRET" {
		t.Errorf("the migrated factor holds %q — the user's authenticator would stop working", factors[0].Secret)
	}
	// The replay watermark has to come across, or the code showing on the user's
	// phone right now becomes replayable for the rest of its step.
	if factors[0].LastCounter != 42 {
		t.Errorf("last counter %d, want 42 carried over", factors[0].LastCounter)
	}
	if factors[0].Kind != FactorKindTOTP || factors[0].Name == "" {
		t.Errorf("migrated factor should be a named totp factor: %+v", factors[0])
	}

	// And the old column is emptied: a live secret that nothing reads and nobody
	// can remove is a credential nobody knows exists.
	var legacy string
	if err := st.db.QueryRowContext(ctx, `SELECT totp_secret FROM users WHERE id = ?`, uid).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != "" {
		t.Errorf("SECURITY: the old secret is still on the user row (%q) — removable by nobody", legacy)
	}

	u, _ := st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Error("the migrated account should still read as having 2FA")
	}
}

// Migration runs on every start, so running it twice must not pair the same
// authenticator twice.
func TestMigrateTOTPToFactorsIsIdempotent(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = 'LEGACYSECRET', totp_enabled = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := st.migrateTOTPToFactors(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Errorf("three migration runs produced %d factors, want 1", n)
	}
}

// An account that never enrolled must not come out of the migration holding an
// empty factor — which would read as "2FA is on" while accepting no code at all.
func TestMigrateLeavesAccountsWithoutTOTPAlone(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()

	// Also the half-state: a secret stored but never confirmed.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = 'UNCONFIRMED', totp_enabled = 0 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	if err := st.migrateTOTPToFactors(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 0 {
		t.Errorf("an unconfirmed enrolment was migrated into %d factor(s)", n)
	}
	u, _ := st.UserByID(ctx, uid)
	if u.TOTPEnabled {
		t.Error("an account with no confirmed authenticator must not read as protected")
	}
}

// The watermark must only ever move forward: that is what makes a code unusable
// twice inside its step.
func TestBurnFactorCounterOnlyMovesForward(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	id, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone", Secret: "S"})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.BurnFactorCounter(ctx, id, 100); err != nil {
		t.Fatal(err)
	}
	if err := st.BurnFactorCounter(ctx, id, 99); err != nil {
		t.Fatal(err)
	}
	f, err := st.FactorByID(ctx, id, uid)
	if err != nil {
		t.Fatal(err)
	}
	if f.LastCounter != 100 {
		t.Errorf("SECURITY: the watermark went backwards to %d — an older code becomes usable again", f.LastCounter)
	}
	if f.LastUsedAt.IsZero() {
		t.Error("burning a counter should record that the factor was used")
	}
}

// FactorByID is scoped: another account's factor id must not resolve.
func TestFactorByIDIsScopedToItsOwner(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	other, err := st.CreateUser(ctx, &User{Username: "mallory", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone", Secret: "S"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FactorByID(ctx, id, other); err != ErrNotFound {
		t.Errorf("SECURITY: another account resolved this factor (%v)", err)
	}
	if err := st.DeleteFactor(ctx, id, other); err != ErrNotFound {
		t.Errorf("SECURITY: another account's delete reported %v, want not found", err)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Error("SECURITY: the factor was deleted by someone who does not own it")
	}
}

// The name is the owner's own text, shown back to them. Bound it, and give the
// unnamed case something readable rather than an empty row.
func TestFactorNameIsBoundedAndDefaulted(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "   ", Secret: "S"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: strings.Repeat("N", 500), Secret: "S"}); err != nil {
		t.Fatal(err)
	}
	factors, _ := st.ListFactors(ctx, uid)
	if factors[0].Name == "" {
		t.Error("an unnamed authenticator should get a default name")
	}
	if len(factors[1].Name) != factorNameMax {
		t.Errorf("name stored at %d chars, want it capped at %d", len(factors[1].Name), factorNameMax)
	}
}

// Deleting an account takes its factors with it — the secrets are credentials,
// and orphaned rows keep them alive with nothing pointing at them.
func TestDeleteUserTakesItsFactors(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone", Secret: "S"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 0 {
		t.Errorf("a deleted account left %d factor(s) behind", n)
	}
}

// CreatedAt is what the profile shows as "added"; an unparseable stamp would
// render as the zero time.
func TestFactorCreatedAtIsSet(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone", Secret: "S"}); err != nil {
		t.Fatal(err)
	}
	factors, _ := st.ListFactors(ctx, uid)
	if time.Since(factors[0].CreatedAt) > time.Minute {
		t.Errorf("created at %v, which is not now", factors[0].CreatedAt)
	}
}
