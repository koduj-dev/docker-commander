package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

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
	// The owner asked for this. It is off until they do — see
	// TestPentestPasswordlessIsOffUntilTheOwnerAsks.
	if err := st.SetPasswordless(context.Background(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	u, err = st.UserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, u, device
}

// signInPasswordless runs the whole exchange and returns what the service said.
func signInPasswordless(t *testing.T, svc *Service, device *webauthntest.Device, rlKey string) (*LoginResult, error) {
	t.Helper()
	assertion, id, err := svc.BeginPasswordlessLogin("10.0.0.99", testRP())
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

	assertion, id, err := svc.BeginPasswordlessLogin("10.0.0.99", testRP())
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

	assertion, _, err := svc.BeginPasswordlessLogin("10.0.0.99", testRP())
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

// PENTEST: starting a sign-in is metered, and metering means spending.
//
// This endpoint is public and allocates, so a check that only READS the budget
// bounds nothing — which is exactly what an earlier version of this did.
func TestPentestPasswordlessBeginIsMetered(t *testing.T) {
	svc, _, _, _ := passwordlessFixture(t)

	for i := 0; i < 5; i++ {
		if _, _, err := svc.BeginPasswordlessLogin("10.0.0.9", testRP()); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, _, err := svc.BeginPasswordlessLogin("10.0.0.9", testRP()); !errors.Is(err, ErrRateLimited) {
		t.Errorf("SECURITY: the 6th start was not metered (%v)", err)
	}
	// A different address is unaffected: the bucket is per client.
	if _, _, err := svc.BeginPasswordlessLogin("10.0.0.10", testRP()); err != nil {
		t.Errorf("another address should be unaffected: %v", err)
	}
}

// PENTEST: a junk assertion costs the caller login budget. Without it the endpoint
// is a free oracle.
func TestPentestPasswordlessJunkAssertionsBurnBudget(t *testing.T) {
	svc, _, _, _ := passwordlessFixture(t)
	ctx := context.Background()
	impostor := webauthntest.New(t)

	for i := 0; i < 5; i++ {
		assertion, id, err := svc.BeginPasswordlessLogin("10.0.0.11", testRP())
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		body := impostor.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
		if _, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.11", id, webauthntest.Request(body), SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
			t.Fatalf("attempt %d: want ErrInvalidCreds, got %v", i+1, err)
		}
	}
	// The password form from the same address is now refused too — a junk assertion
	// is an attack, and attacks share the login budget.
	if _, err := svc.Login(ctx, "10.0.0.11", "alice", "correcthorse123", false, SessionInfo{}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("SECURITY: junk assertions did not cost the login budget (%v)", err)
	}
}

// …but a refusal that happens AFTER a valid signature must not.
//
// The sign-in button is offered to everyone, so "try it and find out it is off" is
// the ordinary path, not an attack. Charging it to the login bucket means five taps
// lock the password form — and behind one shared address, for everybody.
func TestPasswordlessRefusalsDoNotLockThePasswordForm(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")
	handle, err := st.WebAuthnHandle(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	device.UserHandle = handle // paired, but never opted in

	for i := 0; i < 5; i++ {
		if _, err := signInPasswordless(t, svc, device, "10.0.0.12"); !errors.Is(err, ErrPasswordlessNotAllowed) {
			t.Fatalf("attempt %d: want ErrPasswordlessNotAllowed, got %v", i+1, err)
		}
	}
	if _, err := svc.Login(ctx, "10.0.0.12", "alice", "correcthorse123", false, SessionInfo{}); errors.Is(err, ErrRateLimited) {
		t.Error("SECURITY: honest passkey attempts locked the password form")
	}
}

// PENTEST: it is off until the account holder turns it on.
//
// A passkey paired as a SECOND factor was accepted on the understanding that the
// password still stood in front of it. Letting it become a whole login because the
// app was updated would change what the account rests on without anybody deciding
// to — and for a synced passkey it moves that to the platform account the key syncs
// through, which is a different threat model, not a smaller one.
func TestPentestPasswordlessIsOffUntilTheOwnerAsks(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")
	handle, err := st.WebAuthnHandle(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	device.UserHandle = handle

	// Freshly paired, nothing opted into: the default.
	fresh, err := st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Passwordless {
		t.Fatal("SECURITY: a new account defaults to passwordless sign-in")
	}
	if _, err := signInPasswordless(t, svc, device, "10.0.0.20"); !errors.Is(err, ErrPasswordlessNotAllowed) {
		t.Errorf("SECURITY: a passkey signed in an account that never asked for it (%v)", err)
	}

	// …and once asked for, it works. The guard is consent, not a ban.
	if err := st.SetPasswordless(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := signInPasswordless(t, svc, device, "10.0.0.21"); err != nil {
		t.Errorf("an account that opted in should sign in: %v", err)
	}
}

// A refusal that happens AFTER the assertion verified knows whose account it was,
// and has to say so — a cloned-key detection that reaches no audit log is a warning
// nobody receives.
func TestPasswordlessRefusalsCarryTheAccount(t *testing.T) {
	svc, _, u, device := passwordlessFixture(t)

	if _, err := signInPasswordless(t, svc, device, "10.0.0.22"); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	device.SignCount = 0

	_, err := signInPasswordless(t, svc, device, "10.0.0.23")
	var named *PasswordlessError
	if !errors.As(err, &named) {
		t.Fatalf("a cloned key returned %v, which names no account", err)
	}
	if named.Username != u.Username {
		t.Errorf("the failure names %q, want %q", named.Username, u.Username)
	}
	if !errors.Is(err, ErrClonedAuthenticator) {
		t.Errorf("wrapping lost the cause: %v", err)
	}
}

// The ceremony map is bounded: this endpoint is public and allocates.
func TestPasswordlessCeremoniesAreBounded(t *testing.T) {
	c := newCeremonies(maxPublicCeremonies)
	for i := 0; i < maxPublicCeremonies; i++ {
		if !c.put(fmt.Sprintf("k%d", i), webauthn.SessionData{}, true) {
			t.Fatalf("refused at %d, below the cap", i)
		}
	}
	if c.put("one-too-many", webauthn.SessionData{}, true) {
		t.Error("SECURITY: the ceremony map grew past its cap")
	}
	// Replacing an existing key still works, so a flood cannot block someone's own
	// second attempt at a ceremony they already own.
	if !c.put("k0", webauthn.SessionData{}, true) {
		t.Error("replacing an existing ceremony was refused")
	}
}

// PENTEST: the second user-verification lock, on its own.
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

	assertion, id, err := svc.beginPasswordlessLogin("10.0.0.13", testRP(), protocol.VerificationPreferred)
	if err != nil {
		t.Fatal(err)
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())

	res, err := svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.13", id, webauthntest.Request(body), SessionInfo{})
	if !errors.Is(err, ErrUserVerificationRequired) {
		t.Fatalf("SECURITY: want ErrUserVerificationRequired, got %v", err)
	}
	if res != nil {
		t.Error("SECURITY: a session was issued for an unverified assertion")
	}
}

// PENTEST: the rule is "accounts this server owns the password for", not "not LDAP".
//
// A denylist passes the LDAP test and still lets the next auth source through by
// default. This pins the allowlist with a source that is neither.
func TestPentestPasswordlessRefusesAnUnknownAuthSource(t *testing.T) {
	svc, st, _ := passkeyFixture(t)
	ctx := context.Background()

	uid, err := st.CreateUser(ctx, &store.User{
		Username: "federated", Role: "user", AuthSource: "oidc", PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	device := pairPasskey(t, svc, u, "Federated laptop")
	handle, err := st.WebAuthnHandle(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	device.UserHandle = handle
	// Even opted in, an account this server does not own the password for is out.
	if err := st.SetPasswordless(ctx, uid, true); err != nil {
		t.Fatal(err)
	}

	if _, err := signInPasswordless(t, svc, device, "10.0.0.14"); !errors.Is(err, ErrPasswordlessNotAllowed) {
		t.Errorf("SECURITY: an %q account signed in with a passkey alone (%v)", "oidc", err)
	}
}

// A failure BEFORE the signature verifies must not name an account.
//
// The library resolves the user handle — attacker-chosen, unsigned at that point —
// before it checks the signature. Naming that account would let anyone write failed
// sign-in lines into the audit log against a name they guessed.
func TestPasswordlessDoesNotNameAnAccountBeforeVerifying(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()
	handle, err := st.WebAuthnHandle(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	// An authenticator this server has never seen, waving the victim's handle.
	impostor := webauthntest.New(t)
	impostor.UserHandle = handle
	impostor.UserVerified = false

	assertion, id, err := svc.BeginPasswordlessLogin("10.0.0.15", testRP())
	if err != nil {
		t.Fatal(err)
	}
	body := impostor.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
	_, err = svc.FinishPasswordlessLogin(ctx, testRP(), "10.0.0.15", id, webauthntest.Request(body), SessionInfo{})

	var named *PasswordlessError
	if errors.As(err, &named) {
		t.Errorf("SECURITY: an unsigned attempt named %q in an auditable error", named.Username)
	}
}
