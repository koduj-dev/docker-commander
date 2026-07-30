package auth

import (
	"context"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Re-pairing an authenticator must never weaken the one that already works.
// Starting the flow used to overwrite the live secret and set enabled=false, so
// simply closing the dialog left the account with 2FA off and the authenticator
// in the user's hand no longer valid. That was harmless while enrolment was the
// only caller; a "pair another authenticator" button on the profile page makes it
// an everyday path.

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

// enrol completes a first-time enrolment and returns the active secret.
func enrol(t *testing.T, svc *Service, st *store.Store, uid int64) string {
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
	if err := svc.ConfirmTOTPEnrollment(ctx, uid, code); err != nil {
		t.Fatalf("confirm first enrolment: %v", err)
	}
	u, _ := st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Fatal("setup: 2FA should be enabled after enrolment")
	}
	return enr.Secret
}

// PENTEST: abandoning a re-pair must leave the existing authenticator working and
// 2FA enabled — no silent downgrade.
func TestPentestAbandonedRepairKeepsExisting2FA(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	original := enrol(t, svc, st, uid)

	// Start a re-pair and walk away.
	if _, err := svc.BeginTOTPEnrollment(ctx, uid); err != nil {
		t.Fatal(err)
	}

	u, _ := st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Error("SECURITY: an abandoned re-pair disabled 2FA")
	}
	if u.TOTPSecret != original {
		t.Error("SECURITY: an abandoned re-pair replaced the working secret")
	}
	// The authenticator the user still holds keeps producing valid codes.
	code, err := currentCode(original)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateTOTP(code, u.TOTPSecret) {
		t.Error("SECURITY: the existing authenticator stopped working")
	}
}

// PENTEST: a wrong code during a re-pair must not promote the new secret.
func TestPentestFailedRepairKeepsExisting2FA(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	original := enrol(t, svc, st, uid)

	if _, err := svc.BeginTOTPEnrollment(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmTOTPEnrollment(ctx, uid, "000000"); err == nil {
		t.Fatal("a wrong code should be refused")
	}
	u, _ := st.UserByID(ctx, uid)
	if u.TOTPSecret != original || !u.TOTPEnabled {
		t.Error("SECURITY: a failed re-pair changed the active authenticator")
	}
}

// PENTEST: a code from the OLD authenticator must not confirm a re-pair — that
// would leave the user believing the new device is paired when it isn't.
func TestPentestRepairRejectsOldAuthenticatorCode(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	original := enrol(t, svc, st, uid)

	enr, err := svc.BeginTOTPEnrollment(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	oldCode, err := currentCode(original)
	if err != nil {
		t.Fatal(err)
	}
	// Only meaningful when the two secrets actually produce different codes.
	newCode, err := currentCode(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if oldCode == newCode {
		t.Skip("the two secrets happened to produce the same code this interval")
	}
	if err := svc.ConfirmTOTPEnrollment(ctx, uid, oldCode); err == nil {
		t.Error("SECURITY: a code from the old authenticator confirmed the re-pair")
	}
	u, _ := st.UserByID(ctx, uid)
	if u.TOTPSecret != original {
		t.Error("SECURITY: the secret changed despite the failed confirmation")
	}
}

// The happy path: confirming with the new authenticator promotes it, and the old
// one stops working.
func TestRepairPromotesTheNewAuthenticator(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()
	original := enrol(t, svc, st, uid)

	enr, err := svc.BeginTOTPEnrollment(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	code, err := currentCode(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmTOTPEnrollment(ctx, uid, code); err != nil {
		t.Fatalf("confirm re-pair: %v", err)
	}

	u, _ := st.UserByID(ctx, uid)
	if !u.TOTPEnabled {
		t.Error("2FA should stay enabled after a successful re-pair")
	}
	if u.TOTPSecret != enr.Secret {
		t.Error("the new secret should now be active")
	}
	if u.TOTPPending != "" {
		t.Error("the pending secret should be cleared")
	}
	if u.TOTPSecret == original {
		t.Error("the secret did not actually change")
	}
}

// First-time enrolment is unchanged: the secret goes straight in, disabled until
// confirmed, with nothing left pending.
func TestFirstEnrolmentUnchanged(t *testing.T) {
	svc, st, uid := totpFixture(t)
	ctx := context.Background()

	enr, err := svc.BeginTOTPEnrollment(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(ctx, uid)
	if u.TOTPSecret != enr.Secret || u.TOTPEnabled {
		t.Errorf("first enrolment should store the secret disabled: enabled=%v", u.TOTPEnabled)
	}
	if u.TOTPPending != "" {
		t.Error("first enrolment should leave nothing pending")
	}
}

// currentCode produces the code an authenticator would show right now for a
// secret, so the tests can complete a real enrolment rather than mocking it.
func currentCode(secret string) (string, error) {
	return totp.GenerateCode(secret, time.Now().UTC())
}
