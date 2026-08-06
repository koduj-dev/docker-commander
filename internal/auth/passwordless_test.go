package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/webauthntest"
)

// passwordlessFixture pairs a passkey and hands back a device that knows the
// account's user handle — which is what makes it discoverable, and so what makes it
// answerable before anyone has said who they are.
func passwordlessFixture(t *testing.T) (*Service, *store.Store, *store.User, *webauthntest.Device) {
	t.Helper()
	svc, st, u := passkeyFixture(t)
	device := pairPasskey(t, svc, u, "Laptop")
	handle, err := st.WebAuthnHandle(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	device.UserHandle = handle
	return svc, st, u, device
}

// signInPasswordless runs the whole exchange and returns what the service said.
func signInPasswordless(t *testing.T, svc *Service, device *webauthntest.Device, rlKey string) (*LoginResult, error) {
	t.Helper()
	assertion, id, err := svc.BeginPasswordlessLogin(testRP())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
	return svc.FinishPasswordlessLogin(context.Background(), testRP(), rlKey, id,
		webauthntest.Request(body), SessionInfo{})
}

// The whole point: a passkey, and nothing else, signs the account in.
func TestPasswordlessRoundTrip(t *testing.T) {
	svc, _, u, device := passwordlessFixture(t)

	res, err := signInPasswordless(t, svc, device, "10.0.0.1")
	if err != nil {
		t.Fatalf("passwordless sign-in: %v", err)
	}
	if res.MFARequired {
		t.Error("a user-verified passkey is both factors; it must not ask for another")
	}
	if res.User.ID != u.ID {
		t.Errorf("signed in as %d, want %d", res.User.ID, u.ID)
	}
	if res.Token == "" {
		t.Error("no session token")
	}
}

// PENTEST: without user verification the assertion proves possession and nothing
// else — one factor. A key found in a drawer must not be a login.
//
// This is the guard the whole feature rests on. The browser is ASKED for user
// verification, but an authenticator may answer without it, so the flag on the
// assertion is what decides.
func TestPentestPasswordlessRefusesAnUnverifiedAssertion(t *testing.T) {
	svc, _, _, device := passwordlessFixture(t)
	device.UserVerified = false

	// Two locks, and this exercises them together: the ceremony demands user
	// verification, so the library refuses before the flag is ever inspected. What
	// matters here is that nothing gets in.
	res, err := signInPasswordless(t, svc, device, "10.0.0.2")
	if err == nil {
		t.Fatal("SECURITY: an assertion with no user verification signed in")
	}
	if res != nil {
		t.Error("SECURITY: a session was issued anyway")
	}

	// …and the same key WITH verification still works, so the refusal is about the
	// missing PIN rather than something incidental about this device.
	device.UserVerified = true
	if _, err := signInPasswordless(t, svc, device, "10.0.0.3"); err != nil {
		t.Errorf("a verified assertion should sign in: %v", err)
	}
}

// PENTEST: one challenge, one attempt. A captured assertion must not be replayable.
func TestPentestPasswordlessCeremonyIsSingleUse(t *testing.T) {
	svc, _, _, device := passwordlessFixture(t)
	ctx := context.Background()

	assertion, id, err := svc.BeginPasswordlessLogin(testRP())
	if err != nil {
		t.Fatal(err)
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())

	if _, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.4", id, webauthntest.Request(body), SessionInfo{}); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if _, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.4", id, webauthntest.Request(body), SessionInfo{}); err == nil {
		t.Error("SECURITY: a captured assertion was replayed")
	}
}

// PENTEST: a ceremony id nobody issued buys nothing.
func TestPentestPasswordlessRejectsAnUnknownCeremony(t *testing.T) {
	svc, _, _, device := passwordlessFixture(t)
	ctx := context.Background()

	assertion, _, err := svc.BeginPasswordlessLogin(testRP())
	if err != nil {
		t.Fatal(err)
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())

	for _, id := range []string{"", "not-a-ceremony", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.5", id, webauthntest.Request(body), SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
			t.Errorf("ceremony id %q returned %v, want ErrInvalidCreds", id, err)
		}
	}
}

// PENTEST: a rewound counter means the key answered from two places. Refused here
// as everywhere else — and here it matters more, because this assertion is the
// whole login rather than its second half.
func TestPentestPasswordlessRefusesAClonedKey(t *testing.T) {
	svc, _, _, device := passwordlessFixture(t)

	if _, err := signInPasswordless(t, svc, device, "10.0.0.6"); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	device.SignCount = 0 // the copy has not kept up

	if _, err := signInPasswordless(t, svc, device, "10.0.0.7"); !errors.Is(err, ErrClonedAuthenticator) {
		t.Errorf("SECURITY: a rewound counter was accepted (%v)", err)
	}
}

// PENTEST: an LDAP account's authority is the directory. A passkey answers none of
// the questions the directory does — whether the account still exists, whether it
// is disabled — so it must not be a way around it.
func TestPentestPasswordlessRefusesAnLDAPAccount(t *testing.T) {
	svc, st, _ := passkeyFixture(t)
	ctx := context.Background()

	// An account the directory owns, with a passkey of its own.
	uid, err := st.CreateUser(ctx, &store.User{
		Username: "directory", Role: "user", AuthSource: "ldap", PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	ldapUser, err := st.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	device := pairPasskey(t, svc, ldapUser, "Directory laptop")
	handle, err := st.WebAuthnHandle(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	device.UserHandle = handle

	if _, err := signInPasswordless(t, svc, device, "10.0.0.8"); !errors.Is(err, ErrPasswordlessNotAllowed) {
		t.Errorf("SECURITY: an LDAP account signed in with a passkey alone (%v)", err)
	}
}

// PENTEST: failures burn the budget. There is no account to bucket on until the
// assertion verifies, so without this the endpoint is a free oracle.
func TestPentestPasswordlessIsRateLimited(t *testing.T) {
	svc, _, _, _ := passwordlessFixture(t)
	ctx := context.Background()

	// An authenticator this server has never seen.
	impostor := webauthntest.New(t)
	for i := 0; i < 5; i++ {
		assertion, id, err := svc.BeginPasswordlessLogin(testRP())
		if err != nil {
			t.Fatal(err)
		}
		body := impostor.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
		if _, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.9", id, webauthntest.Request(body), SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
			t.Fatalf("attempt %d: want ErrInvalidCreds, got %v", i+1, err)
		}
	}

	assertion, id, err := svc.BeginPasswordlessLogin(testRP())
	if err != nil {
		t.Fatal(err)
	}
	body := impostor.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
	if _, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.9", id, webauthntest.Request(body), SessionInfo{}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("SECURITY: the 6th attempt was not rate limited (%v)", err)
	}
}

// PENTEST: the second lock, on its own.
//
// With user verification demanded of the ceremony, the library refuses first and
// the check on the returned flag is never reached — so a test that only runs the
// normal path proves nothing about it. This weakens the ceremony to "preferred",
// which is what the code would do if that demand were ever dropped, and shows the
// flag check still refuses.
func TestPentestPasswordlessFlagCheckStandsAlone(t *testing.T) {
	svc, _, _, device := passwordlessFixture(t)
	ctx := context.Background()
	device.UserVerified = false

	assertion, id, err := svc.beginPasswordlessLogin(testRP(), protocol.VerificationPreferred)
	if err != nil {
		t.Fatal(err)
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())

	res, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.10", id, webauthntest.Request(body), SessionInfo{})
	if !errors.Is(err, ErrUserVerificationRequired) {
		t.Fatalf("SECURITY: want ErrUserVerificationRequired, got %v", err)
	}
	if res != nil {
		t.Error("SECURITY: a session was issued for an unverified assertion")
	}
}
