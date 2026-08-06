package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// ErrPasswordlessNotAllowed means the account is not one this may sign in.
var ErrPasswordlessNotAllowed = errors.New("auth: this account must sign in with its password")

// passwordlessKey names a ceremony that no session and no token identifies yet.
func passwordlessKey(id string) string { return "passwordless:" + id }

// BeginPasswordlessLogin issues a discoverable-credential challenge.
//
// Nobody has said who they are, so this cannot be scoped to an account: the
// authenticator is asked what it holds for this relying party. The returned id is
// what ties the answer back to this challenge — an opaque, single-use handle, since
// there is no session and no half-finished login to key it on.
func (s *Service) BeginPasswordlessLogin(rp RelyingParty) (*protocol.CredentialAssertion, string, error) {
	// Required, not preferred. The whole argument for signing in without a password
	// is that the authenticator checked a PIN or a fingerprint; asking politely and
	// accepting "no" would leave possession alone standing in for both factors.
	return s.beginPasswordlessLogin(rp, protocol.VerificationRequired)
}

// beginPasswordlessLogin takes the user-verification requirement as a parameter so
// a test can weaken it. That is not a knob for production: it exists to prove that
// the check on the RETURNED flag stands on its own, because with the requirement in
// place the library refuses first and the second layer is never reached.
func (s *Service) beginPasswordlessLogin(rp RelyingParty, uv protocol.UserVerificationRequirement) (*protocol.CredentialAssertion, string, error) {
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
	s.ceremonies.put(passwordlessKey(id), *session, true)
	return assertion, id, nil
}

// FinishPasswordlessLogin verifies the assertion and issues a session.
//
// rlKey is the rate-limit bucket — the client address, because until the assertion
// verifies there is no account to bucket on. That is also why this must not become
// a free oracle: every failure costs the caller budget.
func (s *Service) FinishPasswordlessLogin(ctx context.Context, rp RelyingParty, rlKey, ceremonyID string, r *http.Request, info SessionInfo) (*LoginResult, error) {
	if !s.limiter.Allow(rlKey) {
		return nil, ErrRateLimited
	}
	api, err := rp.webauthnAPI()
	if err != nil {
		return nil, err
	}
	if ceremonyID == "" {
		s.limiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	cer, ok := s.ceremonies.take(passwordlessKey(ceremonyID))
	if !ok {
		s.limiter.Fail(rlKey)
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

	credential, err := api.FinishDiscoverableLogin(handler, cer.data, r)
	if err != nil || account == nil {
		s.limiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}

	// The flag, not the request. The ceremony above demands user verification and
	// the library enforces that, so in production this is the second of two locks —
	// but it is the one that does not depend on the ceremony having been set up
	// correctly. Without UV the assertion proves possession only, which is one
	// factor, and one factor is not a login.
	if !credential.Flags.UserVerified {
		s.limiter.Fail(rlKey)
		return nil, ErrUserVerificationRequired
	}
	if credential.Authenticator.CloneWarning {
		s.limiter.Fail(rlKey)
		return nil, ErrClonedAuthenticator
	}
	// An LDAP account's authority is the directory: whether it still exists, whether
	// it is disabled, what it may do. A passkey here would answer none of those and
	// would keep working after the directory said no.
	if account.AuthSource == "ldap" {
		s.limiter.Fail(rlKey)
		return nil, ErrPasswordlessNotAllowed
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

	s.limiter.Reset(rlKey)
	return s.issueSession(ctx, account, info)
}
