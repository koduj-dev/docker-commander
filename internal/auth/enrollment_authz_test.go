package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/webauthntest"
)

// Adding a factor to a protected account needs the password. The check has to
// happen where the factor is CREATED, not only where the enrolment starts —
// otherwise the gap between the two is the hole.
//
// The attack these tests describe: a stolen session on an account that has no
// second factor yet. Starting an enrolment is rightly allowed (there is nothing to
// protect), so the attacker starts one and stops. The owner then protects the
// account. If the half-finished enrolment is still redeemable, the attacker
// completes it afterwards and owns a second factor on a protected account — with a
// session and no password, which is exactly what the step-up exists to prevent.

// PENTEST: a TOTP enrolment stashed before the account was protected must not be
// redeemable after a passkey is paired.
func TestPentestStashedTOTPEnrolmentDiesWhenAPasskeyArrives(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()

	// The attacker starts an enrolment while there is nothing to protect.
	enr, err := svc.BeginTOTPEnrollment(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}

	// The owner protects the account.
	pairPasskey(t, svc, u, "Owner's laptop")

	// The stash must be worthless now, on both counts: cleared outright, and
	// refused even if it were somehow still there.
	after, err := st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TOTPPending != "" {
		t.Errorf("SECURITY: pairing a passkey left a redeemable enrolment behind (%q)", after.TOTPPending)
	}

	code, err := currentCode(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.ConfirmTOTPEnrollment(ctx, u.ID, code, "Attacker's phone")
	if err == nil {
		t.Fatal("SECURITY: a session alone added an authenticator to a protected account")
	}
	if n, _ := st.CountFactors(ctx, u.ID); n != 1 {
		t.Errorf("SECURITY: the account holds %d factors, want only the owner's passkey", n)
	}
}

// The same guard, reached directly: an enrolment that was never authorised with the
// password is refused once the account has a factor, whatever cleared the stash.
func TestUnauthorisedEnrolmentIsRefusedOnceTheAccountIsProtected(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()

	// A factor exists...
	if _, err := st.CreateFactor(ctx, &store.AuthFactor{
		UserID: u.ID, Kind: store.FactorKindPasskey, Name: "Laptop", CredentialID: "cred-1", Credential: "{}",
	}, true); err != nil {
		t.Fatal(err)
	}
	// ...and a pending enrolment that nobody proved the password for.
	if err := st.SetTOTPPending(ctx, u.ID, "JBSWY3DPEHPK3PXP", false); err != nil {
		t.Fatal(err)
	}
	code, err := currentCode("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ConfirmTOTPEnrollment(ctx, u.ID, code, "Impostor"); !errors.Is(err, ErrEnrollmentStale) {
		t.Fatalf("want ErrEnrollmentStale, got %v", err)
	}
	if n, _ := st.CountFactors(ctx, u.ID); n != 1 {
		t.Errorf("SECURITY: an unauthorised enrolment paired a factor (%d total)", n)
	}

	// The same enrolment, authorised with the password, is fine — the guard is
	// about authority, not about pending secrets being suspicious.
	if err := st.SetTOTPPending(ctx, u.ID, "JBSWY3DPEHPK3PXP", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmTOTPEnrollment(ctx, u.ID, code, "Owner's phone"); err != nil {
		t.Fatalf("an authorised enrolment should complete: %v", err)
	}
}

// PENTEST: the passkey half of the same hole. A registration ceremony opened while
// the account had nothing to protect must not land after it gains a factor.
func TestPentestPasskeyCeremonyCannotOutliveTheAccountBecomingProtected(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()

	// Opened with no password, legitimately: the account has no factor yet.
	creation, err := svc.BeginPasskeyRegistration(ctx, testRP(), u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	device := webauthntest.New(t)
	body := device.Register(t, testRPID, testOrigin, creation.Response.Challenge.String())

	// The owner protects the account with a TOTP authenticator. Note this does not
	// disturb the attacker's ceremony: a passkey enrolment and a TOTP one live in
	// different places, which is what makes this reachable.
	if _, err := st.CreateFactor(ctx, &store.AuthFactor{
		UserID: u.ID, Kind: store.FactorKindTOTP, Name: "Owner's phone", Secret: "JBSWY3DPEHPK3PXP",
	}, true); err != nil {
		t.Fatal(err)
	}

	err = svc.FinishPasskeyRegistration(ctx, testRP(), u.ID, "Attacker's key", webauthntest.Request(body))
	if !errors.Is(err, ErrEnrollmentStale) {
		t.Fatalf("SECURITY: want ErrEnrollmentStale, got %v", err)
	}
	if n, _ := st.CountFactors(ctx, u.ID); n != 1 {
		t.Errorf("SECURITY: the ceremony paired a passkey anyway (%d factors)", n)
	}
}

// A ceremony opened WITH the password still completes after the account gains a
// factor — the guard must not break the ordinary "add a second authenticator" flow,
// which is exactly the case where the account is already protected.
func TestAuthorisedPasskeyCeremonySurvivesTheAccountGainingAFactor(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()

	creation, err := svc.BeginPasskeyRegistration(ctx, testRP(), u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	device := webauthntest.New(t)
	body := device.Register(t, testRPID, testOrigin, creation.Response.Challenge.String())

	if _, err := st.CreateFactor(ctx, &store.AuthFactor{
		UserID: u.ID, Kind: store.FactorKindTOTP, Name: "Phone", Secret: "JBSWY3DPEHPK3PXP",
	}, true); err != nil {
		t.Fatal(err)
	}

	if err := svc.FinishPasskeyRegistration(ctx, testRP(), u.ID, "Laptop", webauthntest.Request(body)); err != nil {
		t.Fatalf("an authorised ceremony should complete: %v", err)
	}
	if n, _ := st.CountFactors(ctx, u.ID); n != 2 {
		t.Errorf("the account holds %d factors, want 2", n)
	}
}
