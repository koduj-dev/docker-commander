package store

import (
	"context"
	"errors"
	"strings"
	"sync"
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

// ListFactors is what consumeTOTP walks to check a code. Unscoped, ANY account's
// authenticator would sign you in as anyone — so the scoping needs a test of its
// own down here, not only in the API pentest three packages away.
func TestListFactorsIsScopedToItsOwner(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	other, err := st.CreateUser(ctx, &User{Username: "mallory", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Alice phone", Secret: "ALICE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: other, Name: "Mallory phone", Secret: "MALLORY"}); err != nil {
		t.Fatal(err)
	}

	mine, err := st.ListFactors(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Secret != "ALICE" {
		t.Fatalf("SECURITY: the list is not scoped to its owner: %+v", mine)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Errorf("SECURITY: CountFactors counted %d, want this account's 1", n)
	}
}

// One secret, one row. Two rows sharing a secret would give that authenticator two
// independent watermarks, so each of its codes could be spent twice.
func TestOneFactorPerSecret(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone", Secret: "SAME"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone again", Secret: "SAME"}); err == nil {
		t.Error("SECURITY: the same secret was paired twice for one account")
	}
	// A different account may of course hold its own.
	other, _ := st.CreateUser(ctx, &User{Username: "bob", Role: "user"})
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: other, Name: "Phone", Secret: "SAME"}); err != nil {
		t.Errorf("a different account should be able to hold its own secret: %v", err)
	}
}

// Duplicates that predate the constraint are collapsed rather than taking the
// installation down on start — keeping the highest watermark, so no already-spent
// code becomes usable again by the cleanup itself.
func TestDuplicateFactorsAreCollapsedOnStart(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx, `
		INSERT INTO auth_factors (user_id, kind, name, secret, last_counter, created_at)
		VALUES (?, 'totp', 'A', 'DUP', 10, '2026-01-01T00:00:00Z'),
		       (?, 'totp', 'B', 'DUP', 77, '2026-01-01T00:00:00Z')`, uid, uid); err != nil {
		t.Skipf("the unique index already forbids seeding duplicates: %v", err)
	}

	if err := st.enforceOneFactorPerSecret(ctx); err != nil {
		t.Fatal(err)
	}
	factors, _ := st.ListFactors(ctx, uid)
	if len(factors) != 1 {
		t.Fatalf("want the duplicates collapsed to 1, got %d", len(factors))
	}
	if factors[0].LastCounter != 77 {
		t.Errorf("SECURITY: collapsing kept watermark %d, want the highest (77) — otherwise a spent code works again",
			factors[0].LastCounter)
	}
}

// Pairing is a compare-and-swap on the pending secret: parallel confirmations of
// one enrolment must produce exactly one factor. N factors sharing a secret means
// N watermarks, so every code from that device would be spendable N times.
func TestPairPendingFactorClaimsTheEnrolmentOnce(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if err := st.SetTOTPPending(ctx, uid, "PENDINGSECRET"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	won := make(chan int64, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, err := st.PairPendingFactor(ctx, uid, "PENDINGSECRET", "Phone"); err == nil {
				won <- id
			}
		}()
	}
	wg.Wait()
	close(won)

	if n := len(won); n != 1 {
		t.Errorf("SECURITY: %d of 16 parallel confirmations were accepted, want exactly 1", n)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Errorf("SECURITY: one enrolment produced %d factors", n)
	}
	u, _ := st.UserByID(ctx, uid)
	if u.TOTPPending != "" {
		t.Error("the claimed enrolment should leave nothing pending")
	}
}

// The last-factor guard has to live inside the DELETE. Two concurrent removals
// that both read "2 factors" and both delete leave the account with NONE — and
// zero factors is not a lockout, it is 2FA silently switched off.
func TestConcurrentRemovalsCannotEmptyTheAccount(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	a, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone", Secret: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Tablet", Secret: "B"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, id := range []int64{a, b} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_ = st.DeleteFactor(ctx, id, uid)
		}(id)
	}
	wg.Wait()

	n, _ := st.CountFactors(ctx, uid)
	if n == 0 {
		t.Fatal("SECURITY: concurrent removals emptied the account — the password alone now signs in")
	}
	if n != 1 {
		t.Errorf("want exactly one factor left, got %d", n)
	}
	u, _ := st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Error("SECURITY: the account came out of this with 2FA off")
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
	// Losing the race — the same code presented twice at once — must be reported,
	// not swallowed. The caller turns a nil here into a session, so "nothing to do"
	// and "someone else already spent this step" cannot look alike.
	if err := st.BurnFactorCounter(ctx, id, 99); !errors.Is(err, ErrCounterNotAdvanced) {
		t.Errorf("SECURITY: a burn that moved nothing reported %v, want ErrCounterNotAdvanced", err)
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
	// Distinct secrets: one authenticator is one row, so two rows need two secrets.
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "   ", Secret: "S1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: strings.Repeat("N", 500), Secret: "S2"}); err != nil {
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

// TOTPEnabled says "this account has an authenticator app", and the login path
// treats it as "demand a TOTP challenge". A factor of some other kind must
// therefore not set it: a passkey-only account would otherwise be asked for a code
// no device of theirs can produce — an unrecoverable lockout, arriving the day the
// next factor type lands.
func TestTOTPEnabledCountsOnlyAuthenticatorApps(t *testing.T) {
	st, uid := factorStore(t)
	ctx := context.Background()
	if _, err := st.CreateFactor(ctx, &AuthFactor{
		UserID: uid, Kind: "webauthn", Name: "Passkey", Secret: "CREDENTIAL",
	}); err != nil {
		t.Fatal(err)
	}

	u, err := st.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.TOTPEnabled {
		t.Error("SECURITY: a non-TOTP factor made the account demand a TOTP code it cannot produce")
	}

	// …and adding a real authenticator does set it.
	if _, err := st.CreateFactor(ctx, &AuthFactor{UserID: uid, Name: "Phone", Secret: "S"}); err != nil {
		t.Fatal(err)
	}
	u, _ = st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Error("an authenticator app should switch TOTP on")
	}
}
