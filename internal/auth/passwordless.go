package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Signing in with a passkey alone.
//
// Elsewhere a passkey stands in for the code an authenticator app would produce,
// with the password still first. Here it is the whole login — which is only
// defensible because a passkey used this way is itself two factors: possession of
// the authenticator, and the PIN or fingerprint that unlocks it. That second half
// is USER VERIFICATION, and it is the entire reason this is not "a key in a drawer
// signs you in". It is demanded of the browser AND checked on the assertion that
// comes back, because a request is not a guarantee.
//
// This never replaces the password. The password plus a second factor remains a
// valid way in, so losing the key costs convenience rather than the account — the
// one recovery story this app can honestly offer, having deliberately given admins
// no way to reset someone else's second factor.

// ErrUserVerificationRequired means the authenticator answered without verifying
// the user — no PIN, no biometric. Enough for a second factor, not enough to BE
// the login.
var ErrUserVerificationRequired = errors.New("auth: this passkey did not verify who you are; use your password")

// ErrPasswordlessNotAllowed means the account has not turned this on, or is one
// that may never use it.
var ErrPasswordlessNotAllowed = errors.New("auth: this account must sign in with its password")

// ErrTooBusy means the server is holding as many half-finished ceremonies as it is
// willing to. Reachable without a session, so it must be bounded.
var ErrTooBusy = errors.New("auth: too many sign-ins in flight, try again")

// PasswordlessError carries the account a failed attempt was for.
//
// Once the assertion has verified, the account is known — and the failures after
// that point are the ones worth writing down: a cloned key, a missing PIN, an
// account that has not opted in. Without the username the audit log cannot say who
// any of it happened to, and a cloned-authenticator detection is the last thing that
// should vanish silently.
type PasswordlessError struct {
	Username string
	Err      error
}

func (e *PasswordlessError) Error() string { return e.Err.Error() }
func (e *PasswordlessError) Unwrap() error { return e.Err }

// named wraps err with the account it happened to.
func named(u *store.User, err error) error {
	return &PasswordlessError{Username: u.Username, Err: err}
}

// passwordlessKey names a ceremony that no session and no token identifies yet.
func passwordlessKey(id string) string { return "passwordless:" + id }

// BeginPasswordlessLogin issues a discoverable-credential challenge.
//
// Nobody has said who they are, so this cannot be scoped to an account: the
// authenticator is asked what it holds for this relying party. The returned id is
// what ties the answer back to this challenge — an opaque, single-use handle, since
// there is no session and no half-finished login to key it on.
func (s *Service) BeginPasswordlessLogin(rlKey string, rp RelyingParty) (*protocol.CredentialAssertion, string, error) {
	// Unauthenticated and it allocates, so it is metered — and metering means
	// SPENDING, not asking. Allow only reads the budget; a check without a matching
	// Fail bounds nothing, which is what made an earlier version of this a limit in
	// name only.
	//
	// Its own bucket, not the login one: starting a sign-in is not a guess at a
	// password, and a stranger doing this must not be able to lock the people behind
	// a shared address out of the password form.
	begin := "begin:" + rlKey
	if !s.passkeyLimiter.Allow(begin) {
		return nil, "", ErrRateLimited
	}
	// Required, not preferred. The whole argument for signing in without a password
	// is that the authenticator checked a PIN or a fingerprint; asking politely and
	// accepting "no" would leave possession alone standing in for both factors.
	return s.beginPasswordlessLogin(rlKey, rp, protocol.VerificationRequired)
}

// beginPasswordlessLogin takes the user-verification requirement as a parameter so
// a test can weaken it. That is not a knob for production: it exists to prove that
// the check on the RETURNED flag stands on its own, because with the requirement in
// place the library refuses first and the second layer is never reached.
func (s *Service) beginPasswordlessLogin(rlKey string, rp RelyingParty, uv protocol.UserVerificationRequirement) (*protocol.CredentialAssertion, string, error) {
	api, err := rp.webauthnAPI()
	if err != nil {
		return nil, "", err
	}
	assertion, session, err := api.BeginDiscoverableLogin(webauthn.WithUserVerification(uv))
	if err != nil {
		return nil, "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	if !s.publicCeremonies.put(passwordlessKey(id), *session, true) {
		return nil, "", ErrTooBusy
	}
	// Spent only now that there is something to show for it. Charging before would
	// mean a flood that fills the store also burns the budget of everyone it turned
	// away, leaving them refused for the window after the flood stops.
	s.passkeyLimiter.Fail("begin:" + rlKey)
	return assertion, id, nil
}

// FinishPasswordlessLogin verifies the assertion and issues a session.
//
// rlKey is the rate-limit bucket — the client address, because until the assertion
// verifies there is no account to bucket on. That is also why this must not become
// a free oracle: every failure costs the caller budget.
func (s *Service) FinishPasswordlessLogin(ctx context.Context, rp RelyingParty, rlKey, ceremonyID string, r *http.Request, info SessionInfo) (*LoginResult, error) {
	// Metered in its own bucket, and CHECKED here — a Fail with no matching Allow
	// bounds exactly nothing, which is the mistake this file has now made in both
	// directions. Not the password bucket: an assertion is a signature, not a guess,
	// so failing at it must not close the password form for the address.
	if !s.passkeyLimiter.Allow(rlKey) {
		return nil, ErrRateLimited
	}
	api, err := rp.webauthnAPI()
	if err != nil {
		return nil, err
	}
	if ceremonyID == "" {
		s.passkeyLimiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	cer, ok := s.publicCeremonies.take(passwordlessKey(ceremonyID))
	if !ok {
		s.passkeyLimiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}

	// The library calls this back with the user handle the authenticator returned,
	// and that handle is the only claim of identity in the whole exchange. It is
	// resolved to an account here; the signature check that follows is what makes
	// the claim worth anything.
	var account *store.User
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := s.store.UserByWebAuthnHandle(ctx, userHandle)
		if err != nil {
			return nil, fmt.Errorf("no account for that passkey: %w", err)
		}
		account = u
		return s.passkeyUser(ctx, u)
	}

	// Parsed here rather than handed to the library as a request, so that a missing
	// user verification can be told apart from a bad signature. The library refuses
	// an unverified assertion itself — correctly — but only with a generic error,
	// and "invalid credentials" sends someone with a PIN-less key hunting a problem
	// that is not theirs.
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.passkeyLimiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		s.passkeyLimiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	// Unsigned at this point — anyone can claim anything in these bytes — so it is
	// only ever used to choose the WORDING of a refusal, never to allow anything.
	uvClaimed := parsed.Response.AuthenticatorData.Flags.HasUserVerified()

	_, credential, err := api.ValidatePasskeyLogin(handler, cer.data, parsed)
	if err != nil || account == nil {
		s.passkeyLimiter.Fail(rlKey)
		// Deliberately NOT named: the library calls the handler that resolves the
		// account before it checks the signature, so at this point the account is
		// whoever the unsigned bytes pointed at. Auditing that would let anyone
		// write "failed sign-in" lines against a name they guessed.
		if !uvClaimed {
			return nil, ErrUserVerificationRequired
		}
		return nil, ErrInvalidCreds
	}

	// The flag on the VERIFIED credential. The ceremony demands user verification
	// and the library enforces that, so in production this is the second of two
	// locks — but it is the one that does not depend on the ceremony having been set
	// up correctly. Without UV the assertion proves possession only, which is one
	// factor, and one factor is not a login.
	// From here the signature has already verified, so these are not guesses and
	// must not spend the password-login budget — the button is offered to everyone,
	// and the ordinary "try it and find out it is off" costs five taps. Behind a
	// shared address that would lock a whole office out of the password form. They
	// are still bounded, in a bucket of their own.
	refused := rlKey
	if !credential.Flags.UserVerified {
		s.passkeyLimiter.Fail(refused)
		return nil, named(account, ErrUserVerificationRequired)
	}
	if credential.Authenticator.CloneWarning {
		s.passkeyLimiter.Fail(refused)
		return nil, named(account, ErrClonedAuthenticator)
	}
	// An LDAP account's authority is the directory: whether it still exists, whether
	// it is disabled, what it may do. A passkey here would answer none of those and
	// would keep working after the directory said no.
	//
	// An allowlist, not a denylist: a future auth source should have to be added
	// deliberately rather than inherit this by default.
	if account.AuthSource != "" && account.AuthSource != "local" {
		s.passkeyLimiter.Fail(refused)
		return nil, named(account, ErrPasswordlessNotAllowed)
	}
	// The owner has to have asked for this. Turning a passkey from a second factor
	// into a whole login changes what the account rests on, and for a synced passkey
	// it moves that to the platform account the key syncs through — not a change to
	// make on someone's behalf.
	if !account.Passwordless {
		s.passkeyLimiter.Fail(refused)
		return nil, named(account, ErrPasswordlessNotAllowed)
	}

	// Same as the second-factor path: the counter moves forward or the login fails,
	// because a counter left behind weakens the next login's clone check.
	factor, err := s.store.FactorByCredentialID(ctx, base64.RawURLEncoding.EncodeToString(credential.ID))
	if err != nil {
		return nil, fmt.Errorf("passkey verified but its record could not be read: %w", err)
	}
	if factor.UserID != account.ID {
		// The handle named one account and the credential belongs to another. The
		// unique index makes this unreachable; if it ever lapses, this is the
		// difference between signing in as the wrong person and not at all.
		return nil, ErrInvalidCreds
	}
	blob, err := json.Marshal(credential)
	if err != nil {
		return nil, err
	}
	if err := s.store.UpdateCredential(ctx, factor.ID, string(blob)); err != nil {
		return nil, fmt.Errorf("passkey verified but its counter could not be stored: %w", err)
	}

	// A completed sign-in clears both: the attempts and the button presses that led
	// here were evidently honest.
	s.passkeyLimiter.Reset(rlKey)
	s.passkeyLimiter.Reset("begin:" + rlKey)
	return s.issueSession(ctx, account, info)
}
