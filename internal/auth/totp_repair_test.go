package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Pairing an authenticator must never weaken one that already works.
//
// The flow used to overwrite the live secret and set enabled=false, so simply
// closing the dialog left the account with 2FA off and the authenticator in the
// user's hand no longer valid. It then became "hold the candidate aside until a
// code from it arrives", and now the confirmed candidate is ADDED rather than
// swapped in: an account can hold several authenticators, and pairing the phone
// you just bought must not brick the one in your drawer.
//
// What survives every one of those changes is the invariant these tests exist
// for: nothing that already works may be affected by an incomplete pairing.

func totpFixture(t *testing.T) (*Service, *store.Store, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := NewService(st, NewTokenManager([]byte("0123456789abcdef0123456789abcdef"), 0))
	uid, err := st.CreateUser(context.Background(), &store.User{Username: "u", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, uid
}

// pair completes an enrolment and returns the secret the authenticator holds.
func pair(t *testing.T, svc *Service, st *store.Store, uid int64, name string) string {
	t.Helper()
	ctx := context.Background()
	enr, err := svc.BeginTOTPEnrollment(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	code, err := currentCode(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmTOTPEnrollment(ctx, uid, code, name); err != nil {
		t.Fatalf("confirm enrolment: %v", err)
	}
	u, _ := st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Fatal("2FA should be enabled once an authenticator is paired")
	}
	return enr.Secret
}

// accepts reports whether a code from this secret would sign the account in,
// through the real verification path (which is what "still works" means).
func accepts(t *testing.T, svc *Service, st *store.Store, uid int64, secret string) bool {
	t.Helper()
	code, err := currentCode(secret)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByID(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return svc.consumeTOTP(context.Background(), u, code)
}

// PENTEST: abandoning a pairing must leave the existing authenticator working and
// 2FA enabled — no silent downgrade, and no half-paired device.
func TestPentestAbandonedPairingKeepsExisting2FA(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	original := pair(t, svc, st, uid, "Phone")

	// Start another pairing and walk away.
	if _, err := svc.BeginTOTPEnrollment(ctx, uid); err != nil {
		t.Fatal(err)
	}

	u, _ := st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Error("SECURITY: an abandoned pairing disabled 2FA")
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Errorf("SECURITY: an abandoned pairing left %d factors, want 1", n)
	}
	if !accepts(t, svc, st, uid, original) {
		t.Error("SECURITY: the existing authenticator stopped working")
	}
}

// PENTEST: a wrong code must pair nothing.
func TestPentestFailedPairingAddsNothing(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	original := pair(t, svc, st, uid, "Phone")

	if _, err := svc.BeginTOTPEnrollment(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmTOTPEnrollment(ctx, uid, "000000", "Impostor"); err == nil {
		t.Fatal("a wrong code should be refused")
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Errorf("SECURITY: a failed pairing left %d factors, want 1", n)
	}
	if !accepts(t, svc, st, uid, original) {
		t.Error("SECURITY: a failed pairing disturbed the working authenticator")
	}
}

// PENTEST: a code from an ALREADY-PAIRED authenticator must not confirm a new
// pairing — the user would believe the new device is paired when it isn't, and
// might then throw away the only one that works.
func TestPentestPairingRejectsAnAlreadyPairedCode(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	original := pair(t, svc, st, uid, "Phone")

	enr, err := svc.BeginTOTPEnrollment(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	oldCode, err := currentCode(original)
	if err != nil {
		t.Fatal(err)
	}
	newCode, err := currentCode(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if oldCode == newCode {
		t.Skip("the two secrets happened to produce the same code this interval")
	}
	if err := svc.ConfirmTOTPEnrollment(ctx, uid, oldCode, "Impostor"); err == nil {
		t.Error("SECURITY: a code from the existing authenticator confirmed the pairing")
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Errorf("SECURITY: %d factors after a refused pairing, want 1", n)
	}
}

// The happy path, and the point of the whole change: a second authenticator is
// ADDED. Both work afterwards — that is what makes a lost phone survivable.
func TestPairingASecondAuthenticatorKeepsTheFirst(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	first := pair(t, svc, st, uid, "Phone")
	second := pair(t, svc, st, uid, "Tablet")

	if first == second {
		t.Fatal("the two enrolments produced the same secret")
	}
	if n, _ := st.CountFactors(ctx, uid); n != 2 {
		t.Fatalf("want 2 paired factors, got %d", n)
	}
	if !accepts(t, svc, st, uid, second) {
		t.Error("the newly paired authenticator does not work")
	}
	if !accepts(t, svc, st, uid, first) {
		t.Error("pairing a second authenticator broke the first — the bug this exists to prevent")
	}

	factors, _ := st.ListFactors(ctx, uid)
	if factors[0].Name != "Phone" || factors[1].Name != "Tablet" {
		t.Errorf("names should be the owner's own: %q, %q", factors[0].Name, factors[1].Name)
	}
	if factors[0].LastUsedAt.IsZero() {
		t.Error("using an authenticator should record that it was used")
	}
}

// Nothing is paired until a code from the candidate arrives — including the very
// first enrolment, where there is nothing to protect but also no reason to store
// a secret nobody has proved they hold.
func TestNothingIsPairedBeforeConfirmation(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()

	if _, err := svc.BeginTOTPEnrollment(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 0 {
		t.Errorf("an unconfirmed enrolment paired %d factors", n)
	}
	u, _ := st.UserByID(ctx, uid)
	if u.TOTPEnabled {
		t.Error("2FA must not read as enabled before a code has been accepted")
	}
	if u.TOTPPending == "" {
		t.Error("the candidate secret should be held aside for the confirmation step")
	}
}

// Removing the only second factor is refused: 2FA is mandatory here, so an account
// with none is one that cannot sign in from anywhere but a permitted localhost —
// a self-lockout with no admin reset behind it.
func TestRemovingTheLastFactorIsRefused(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	pair(t, svc, st, uid, "Phone")

	factors, _ := st.ListFactors(ctx, uid)
	if err := svc.RemoveFactor(ctx, uid, factors[0].ID); !errors.Is(err, ErrLastFactor) {
		t.Fatalf("removing the last factor: want ErrLastFactor, got %v", err)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Error("the refused removal took the factor anyway")
	}

	// Pair a replacement and the original can go.
	pair(t, svc, st, uid, "New phone")
	if err := svc.RemoveFactor(ctx, uid, factors[0].ID); err != nil {
		t.Fatalf("removing a factor once another exists: %v", err)
	}
	if n, _ := st.CountFactors(ctx, uid); n != 1 {
		t.Errorf("want 1 factor left, got %d", n)
	}
}

// A removed authenticator stops working immediately — otherwise "remove" is a
// label on a button rather than a thing that happened.
func TestARemovedAuthenticatorStopsWorking(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	first := pair(t, svc, st, uid, "Old phone")
	second := pair(t, svc, st, uid, "New phone")

	factors, _ := st.ListFactors(ctx, uid)
	if err := svc.RemoveFactor(ctx, uid, factors[0].ID); err != nil {
		t.Fatal(err)
	}
	if accepts(t, svc, st, uid, first) {
		t.Error("SECURITY: a removed authenticator still signs the account in")
	}
	if !accepts(t, svc, st, uid, second) {
		t.Error("removing one authenticator broke the other")
	}
}

// PENTEST: the replay watermark is per factor. Sharing one across the account
// would let a code from one device invalidate the same time step on another —
// two authenticators, and whoever used theirs second is refused.
func TestPentestReplayGuardIsPerFactor(t *testing.T) {
	svc, st, uid := totpFixture(t)
	first := pair(t, svc, st, uid, "Phone")
	second := pair(t, svc, st, uid, "Tablet")

	if !accepts(t, svc, st, uid, first) {
		t.Fatal("the first authenticator should be accepted")
	}
	// Same time step, different device: must still be accepted.
	if !accepts(t, svc, st, uid, second) {
		t.Error("a second authenticator was refused because the first had just been used")
	}
	// …and neither code may be replayed.
	if accepts(t, svc, st, uid, first) {
		t.Error("SECURITY: a code was accepted twice")
	}
}

// currentCode produces the code an authenticator would show right now for a
// secret, so the tests can complete a real enrolment rather than mocking it.
func currentCode(secret string) (string, error) {
	return totp.GenerateCode(secret, time.Now().UTC())
}
