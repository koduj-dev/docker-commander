package auth

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/webauthntest"
)

const (
	testRPID   = "docker.example.com"
	testOrigin = "https://docker.example.com"
)

func testRP() RelyingParty {
	return RelyingParty{ID: testRPID, Origin: testOrigin, DisplayName: "Docker Commander"}
}

// passkeyFixture returns a service with an account that already has a password.
func passkeyFixture(t *testing.T) (*Service, *store.Store, *store.User) {
	t.Helper()
	svc, ctx := newService(t)
	u, err := svc.Setup(ctx, "alice", "correcthorse123")
	if err != nil {
		t.Fatal(err)
	}
	return svc, svc.store, u
}

// pairPasskey runs a full registration ceremony and returns the authenticator.
func pairPasskey(t *testing.T, svc *Service, u *store.User, name string) *webauthntest.Device {
	t.Helper()
	ctx := context.Background()
	creation, err := svc.BeginPasskeyRegistration(ctx, testRP(), u.ID, true)
	if err != nil {
		t.Fatalf("begin registration: %v", err)
	}
	device := webauthntest.New(t)
	body := device.Register(t, testRPID, testOrigin, creation.Response.Challenge.String())
	if err := svc.FinishPasskeyRegistration(ctx, testRP(), u.ID, name, webauthntest.Request(body)); err != nil {
		t.Fatalf("finish registration: %v", err)
	}
	return device
}

// signInWithPasskey runs password → challenge → assertion, and returns the result.
func signInWithPasskey(t *testing.T, svc *Service, device *webauthntest.Device) (*LoginResult, error) {
	t.Helper()
	ctx := context.Background()
	res, err := svc.Login(ctx, "10.0.0.1", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !res.MFARequired {
		t.Fatal("SECURITY: an account with a passkey was signed in on the password alone")
	}
	assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token)
	if err != nil {
		return nil, err
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
	return svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(body), SessionInfo{})
}

// The whole round trip, against the real library: register a key, then use it.
func TestPasskeyRoundTrip(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	factors, err := st.ListFactors(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(factors) != 1 || factors[0].Kind != store.FactorKindPasskey {
		t.Fatalf("want one passkey factor, got %+v", factors)
	}
	if factors[0].CredentialID == "" || factors[0].Credential == "" {
		t.Error("the credential was not stored")
	}
	if factors[0].Secret != "" {
		t.Error("a passkey must not carry a shared secret — there is nothing to share")
	}

	// An account holding only a passkey is still an account with 2FA.
	fresh, _ := st.UserByID(ctx, u.ID)
	if !fresh.MFAEnabled {
		t.Error("SECURITY: an account with a passkey does not read as having a second factor")
	}
	if fresh.TOTPEnabled {
		t.Error("a passkey must not read as an authenticator app — the login would ask for a code it cannot produce")
	}

	res, err := signInWithPasskey(t, svc, device)
	if err != nil {
		t.Fatalf("signing in with the passkey: %v", err)
	}
	if res.Token == "" || res.User.ID != u.ID {
		t.Fatalf("the assertion did not produce a session for this account: %+v", res)
	}
}

// PENTEST: a passkey registered for one origin must not work from another.
//
// This is the property that makes WebAuthn phishing-resistant, and the only part
// of it the server can enforce: the signature covers the origin the browser saw.
// A proxy that looks exactly like this app gets an assertion it cannot use here.
func TestPentestPasskeyIsBoundToItsOrigin(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	res, err := svc.Login(ctx, "10.0.0.1", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token)
	if err != nil {
		t.Fatal(err)
	}

	// The same key, the same challenge, signed for the attacker's origin.
	body := device.Assert(t, testRPID, "https://docker-example.com.evil.test", assertion.Response.Challenge.String())
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(body), SessionInfo{}); err == nil {
		t.Error("SECURITY: an assertion signed for another origin was accepted")
	}
}

// PENTEST: an assertion for a different relying-party id must not be accepted.
func TestPentestPasskeyIsBoundToTheRelyingParty(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	res, err := svc.Login(ctx, "10.0.0.1", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token)
	if err != nil {
		t.Fatal(err)
	}

	body := device.Assert(t, "evil.test", testOrigin, assertion.Response.Challenge.String())
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(body), SessionInfo{}); err == nil {
		t.Error("SECURITY: an assertion naming another relying party was accepted")
	}
}

// PENTEST: an assertion is good for the challenge it answers, and no other.
//
// Capturing one and replaying it is the obvious attack; the challenge is what
// stops it, and the ceremony is deleted when it is answered so a second attempt
// has nothing to match against.
func TestPentestPasskeyAssertionCannotBeReplayed(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	res, err := svc.Login(ctx, "10.0.0.1", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token)
	if err != nil {
		t.Fatal(err)
	}
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())

	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(body), SessionInfo{}); err != nil {
		t.Fatalf("the first use should succeed: %v", err)
	}
	// The very same assertion, again.
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(body), SessionInfo{}); err == nil {
		t.Error("SECURITY: a captured assertion was accepted twice")
	}
}

// PENTEST: a fabricated challenge must not be accepted. The server only honours
// the one it issued.
func TestPentestPasskeyRejectsAnUninvitedChallenge(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	res, err := svc.Login(ctx, "10.0.0.1", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token); err != nil {
		t.Fatal(err)
	}

	body := device.Assert(t, testRPID, testOrigin, "Y2hhbGxlbmdlLW9mLW15LW93bi1jaG9vc2luZw")
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(body), SessionInfo{}); err == nil {
		t.Error("SECURITY: an assertion answering a challenge the server never issued was accepted")
	}
}

// PENTEST: one account's passkey must not sign in as another.
func TestPentestPasskeyBelongsToOneAccount(t *testing.T) {
	svc, st, alice := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, alice, "Alice laptop")

	bobID, err := st.CreateUser(ctx, &store.User{Username: "bob", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPassword(ctx, bobID, "correcthorse123"); err != nil {
		t.Fatal(err)
	}
	// Bob needs a factor of his own, or his login would not reach the 2FA step.
	pairPasskey(t, svc, mustUser(t, st, bobID), "Bob laptop")

	res, err := svc.Login(ctx, "10.0.0.1", "bob", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token)
	if err != nil {
		t.Fatal(err)
	}
	// Alice's key, answering Bob's challenge.
	body := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(body), SessionInfo{}); err == nil {
		t.Error("SECURITY: one account's passkey signed in as another")
	}
}

// PENTEST: a signature counter that goes backwards means the key exists twice.
//
// The honest device and the copy are indistinguishable from here, so the only
// useful answer is to refuse and let the owner investigate.
func TestPentestPasskeyRefusesAClonedAuthenticator(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	device := pairPasskey(t, svc, u, "Laptop")

	device.SignCount = 50
	if _, err := signInWithPasskey(t, svc, device); err != nil {
		t.Fatalf("a normal sign-in should work: %v", err)
	}

	// A cloned key still holds a valid private key — it just has not seen the
	// logins the original has.
	device.SignCount = 20
	_, err := signInWithPasskey(t, svc, device)
	if !errors.Is(err, ErrClonedAuthenticator) {
		t.Errorf("SECURITY: a rewound counter was accepted (%v)", err)
	}
}

// The counter moving forward is recorded, or the clone check above would compare
// against whatever the key was first registered with, forever.
func TestPasskeyCounterIsPersisted(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	device.SignCount = 7
	if _, err := signInWithPasskey(t, svc, device); err != nil {
		t.Fatal(err)
	}
	factors, _ := st.ListFactors(ctx, u.ID)
	if factors[0].LastUsedAt.IsZero() {
		t.Error("using a passkey should record that it was used")
	}

	var stored struct {
		Authenticator struct {
			SignCount uint32 `json:"signCount"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal([]byte(factors[0].Credential), &stored); err != nil {
		t.Fatal(err)
	}
	// The device incremented as it signed; whatever it ended on is what must be on
	// disk, or the next login compares against a stale counter and the clone check
	// stops meaning anything.
	if stored.Authenticator.SignCount != device.SignCount {
		t.Errorf("stored counter %d, want the %d the device sent",
			stored.Authenticator.SignCount, device.SignCount)
	}
}

// Passkeys need a secure context. Offering the option where the browser will
// refuse it produces a button that does nothing, so the server says no first.
func TestPasskeysNeedARelyingParty(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	if _, err := svc.BeginPasskeyRegistration(ctx, RelyingParty{}, u.ID, true); !errors.Is(err, ErrPasskeyUnavailable) {
		t.Errorf("registration without a relying party: want ErrPasskeyUnavailable, got %v", err)
	}
}

// An account with no passkey cannot be asked to present one — the browser would
// show a picker with nothing in it.
func TestPasskeyLoginNeedsAPairedKey(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	// Give the account a TOTP factor so login reaches the 2FA step at all.
	enr, err := svc.BeginTOTPEnrollment(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := currentCode(enr.Secret)
	if err := svc.ConfirmTOTPEnrollment(ctx, u.ID, code, "Phone"); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Login(ctx, "10.0.0.1", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token); !errors.Is(err, ErrNoPasskeys) {
		t.Errorf("want ErrNoPasskeys, got %v", err)
	}
}

func mustUser(t *testing.T, st *store.Store, id int64) *store.User {
	t.Helper()
	u, err := st.UserByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// A registration ceremony answers once.
//
// The login path is covered by the challenge token, which is spent on the first
// attempt — so the ceremony's own single-use property is invisible there. Here it
// is the only thing standing between a captured registration response and a second
// credential, so it gets its own test.
func TestPasskeyRegistrationCeremonyIsSingleUse(t *testing.T) {
	svc, st, u := passkeyFixture(t)
	ctx := context.Background()

	creation, err := svc.BeginPasskeyRegistration(ctx, testRP(), u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	device := webauthntest.New(t)
	body := device.Register(t, testRPID, testOrigin, creation.Response.Challenge.String())

	if err := svc.FinishPasskeyRegistration(ctx, testRP(), u.ID, "Laptop", webauthntest.Request(body)); err != nil {
		t.Fatalf("the first registration should succeed: %v", err)
	}
	// The very same response, again. It must be refused because the ceremony is
	// gone — not because the database happened to reject a duplicate id.
	if err := svc.FinishPasskeyRegistration(ctx, testRP(), u.ID, "Laptop again", webauthntest.Request(body)); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("a replayed registration returned %v, want ErrInvalidCreds", err)
	}
	if n, _ := st.CountFactors(ctx, u.ID); n != 1 {
		t.Errorf("a replayed registration produced %d factors, want 1", n)
	}
}

// PENTEST: one password entry, one passkey attempt.
//
// The challenge token is spent on the first FinishPasskeyLogin, right or wrong.
// Without that, a password entry funds an unlimited number of assertion attempts —
// which matters less than for a guessable code, but the rule is the rule, and the
// spend is also what stops a captured assertion being retried against a fresh
// ceremony.
//
// This needs its own test: the replay test above is killed by the ceremony being
// single-use, so it never reaches the token spend at all.
func TestPentestPasskeyChallengeTokenIsSpentOnFirstAttempt(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	ctx := context.Background()
	device := pairPasskey(t, svc, u, "Laptop")

	res, err := svc.Login(ctx, "10.0.0.1", "alice", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}

	// First attempt: answer the wrong challenge, so the assertion is refused.
	if _, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token); err != nil {
		t.Fatal(err)
	}
	wrong := device.Assert(t, testRPID, testOrigin, "bm90LXRoZS1jaGFsbGVuZ2UtaXNzdWVk")
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(wrong), SessionInfo{}); err == nil {
		t.Fatal("an assertion for the wrong challenge should be refused")
	}

	// Second attempt with the SAME token, done properly this time. The token is
	// spent, so it must not produce a session however correct the assertion is.
	assertion, err := svc.BeginPasskeyLogin(ctx, testRP(), res.Token)
	if err != nil {
		t.Fatal(err)
	}
	good := device.Assert(t, testRPID, testOrigin, assertion.Response.Challenge.String())
	if _, err := svc.FinishPasskeyLogin(ctx, testRP(), "10.0.0.1", res.Token, webauthntest.Request(good), SessionInfo{}); err == nil {
		t.Error("SECURITY: a spent challenge token funded a second passkey attempt")
	}
}

// A ceremony expires. Two minutes is long enough for a person and short enough
// that an abandoned one is not a challenge waiting to be answered later.
func TestPasskeyCeremonyExpires(t *testing.T) {
	c := newCeremonies(maxOpenCeremonies)
	c.put("k", webauthn.SessionData{UserID: []byte("u")}, true)

	// Age it past the TTL rather than sleeping through it.
	c.mu.Lock()
	entry := c.open["k"]
	entry.expires = time.Now().Add(-time.Second)
	c.open["k"] = entry
	c.mu.Unlock()

	if _, ok := c.take("k"); ok {
		t.Error("an expired ceremony was still answerable")
	}
	// …and a fresh one is not.
	c.put("k2", webauthn.SessionData{UserID: []byte("u")}, true)
	if _, ok := c.take("k2"); !ok {
		t.Error("a live ceremony should be answerable")
	}
}
