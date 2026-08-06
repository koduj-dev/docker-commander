package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/webauthntest"
)

// fillFactors pairs n factors directly through the store.
//
// Deliberately not through the HTTP flow: each step-up there runs argon2, which is
// what pushed the equivalent TOTP test behind -short — and CI runs -short, so that
// test does not run there at all. A cap that is only checked by a test CI skips is
// a cap nothing checks.
func fillFactors(t *testing.T, st *store.Store, userID int64, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := st.CreateFactor(ctx, &store.AuthFactor{
			UserID:       userID,
			Kind:         store.FactorKindPasskey,
			Name:         fmt.Sprintf("Key %d", i),
			CredentialID: fmt.Sprintf("cred-%d", i),
			Credential:   "{}",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// The cap applies to passkeys too. It is counted across kinds: an account's factors
// are one set, and a cap that only counted authenticator apps would be no cap.
func TestPasskeyRegistrationRefusedAtTheCap(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()
	fillFactors(t, st, u.ID, maxFactorsPerAccount)

	if _, err := svc.BeginPasskeyRegistration(ctx, testRP(), u.ID, true); !errors.Is(err, ErrTooManyFactors) {
		t.Fatalf("want ErrTooManyFactors, got %v", err)
	}
}

// The cap is re-checked when the ceremony lands, not only when it starts: the two
// are minutes apart and the account can fill up in between.
func TestPasskeyRegistrationRefusedWhenTheAccountFillsUpMidCeremony(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()
	fillFactors(t, st, u.ID, maxFactorsPerAccount-1)

	creation, err := svc.BeginPasskeyRegistration(ctx, testRP(), u.ID, true)
	if err != nil {
		t.Fatalf("under the cap the ceremony should start: %v", err)
	}
	device := webauthntest.New(t)
	body := device.Register(t, testRPID, testOrigin, creation.Response.Challenge.String())

	// The last slot goes to something else while this ceremony is open.
	if _, err := st.CreateFactor(ctx, &store.AuthFactor{
		UserID: u.ID, Kind: store.FactorKindTOTP, Name: "Phone", Secret: "JBSWY3DPEHPK3PXP",
	}); err != nil {
		t.Fatal(err)
	}

	err = svc.FinishPasskeyRegistration(ctx, testRP(), u.ID, "One too many", webauthntest.Request(body))
	if !errors.Is(err, ErrTooManyFactors) {
		t.Fatalf("want ErrTooManyFactors, got %v", err)
	}
	if n, _ := st.CountFactors(ctx, u.ID); n != maxFactorsPerAccount {
		t.Errorf("the account holds %d factors, past the cap of %d", n, maxFactorsPerAccount)
	}
}

// PENTEST: a session token must not stand in for an MFA challenge token.
//
// They are both signed by this server, so the only thing telling them apart is the
// kind. Without that check a session — the thing the second factor is supposed to
// gate — would fund the ceremony that grants one.
func TestPentestPasskeyLoginRefusesASessionToken(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	// A genuine session token for this very account: correct signature, correct
	// user, in date. Only the kind is wrong.
	iss, err := svc.tokens.Issue(u.ID, u.Username, u.Role, KindSession, u.SessionEpoch)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.BeginPasskeyLogin(ctx, testRP(), iss.Token); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("SECURITY: a session token opened a passkey challenge (%v)", err)
	}

	// And the finish half, with an assertion that would otherwise be valid — so the
	// refusal is the token kind, not the signature.
	assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), mustChallengeToken(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.2", iss.Token, webauthntest.Request(body), SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("SECURITY: a session token completed a passkey login (%v)", err)
	}
}

// mustChallengeToken runs the password step and returns the MFA challenge token.
func mustChallengeToken(t *testing.T, svc *Service) string {
	t.Helper()
	res, err := svc.Login(context.Background(), "10.0.0.3", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil || !res.MFARequired {
		t.Fatalf("expected an MFA challenge: %+v err=%v", res, err)
	}
	return res.Token
}

// PENTEST: failed passkey assertions must burn the rate-limit budget.
//
// A passkey is not guessable, so this is not about brute force. It is about the
// endpoint not becoming a free oracle: without the limiter it answers unlimited
// probes about which accounts exist and which hold a passkey.
func TestPentestPasskeyLoginIsRateLimited(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	pairPasskey(t, svc, u, "Laptop")

	// An authenticator this account has never seen. Its assertions are well formed
	// and correctly signed — they are simply not from a registered key.
	impostor := webauthntest.New(t)

	for i := 0; i < 5; i++ {
		res, err := svc.Login(ctx, "10.0.0.4", "alice", "correcthorse123", false, SessionInfo{})
		if err != nil || !res.MFARequired {
			t.Fatalf("attempt %d: expected an MFA challenge: %+v err=%v", i+1, res, err)
		}
		assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		body := impostor.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
		if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.4", res.Token, webauthntest.Request(body), SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
			t.Fatalf("attempt %d: an unregistered key returned %v, want ErrInvalidCreds", i+1, err)
		}
	}

	if _, err := svc.Login(ctx, "10.0.0.4", "alice", "correcthorse123", false, SessionInfo{}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("SECURITY: the 6th attempt was not rate limited (%v)", err)
	}
}
