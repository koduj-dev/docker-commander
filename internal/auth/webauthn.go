package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Passkeys, as a second factor.
//
// The account still needs its password; a passkey stands in for the code an
// authenticator app would produce. That is the conservative half of WebAuthn and
// the one worth having here: the private key never leaves the device's secure
// element, and the signature is bound to the origin, so a proxy that looks exactly
// like this app cannot relay it. A TOTP code can be read aloud and typed into the
// wrong window; this cannot.
//
// It is offered, never required — the browser needs a secure context, so on a
// plain-HTTP deployment the option simply is not there (see RelyingParty).

// ErrPasskeyUnavailable means this request could not be a passkey ceremony: the
// browser will not touch WebAuthn outside a secure context, so neither will we.
var ErrPasskeyUnavailable = errors.New("auth: passkeys need HTTPS (or localhost)")

// ErrNoPasskeys means the account has none paired.
var ErrNoPasskeys = errors.New("auth: this account has no passkeys")

// ErrClonedAuthenticator means the signature counter went backwards — the sign of
// a credential that exists in two places.
var ErrClonedAuthenticator = errors.New("auth: this passkey's counter went backwards")

// RelyingParty describes who is asking, in WebAuthn's terms: the id a credential
// is bound to, and the origin it may be used from.
//
// Both are derived per request rather than configured, because getting them wrong
// is not a security failure but a usability one — a credential registered against
// the wrong id simply never works again. The browser is what enforces that the id
// matches the page's origin, so an attacker cannot use a forged Host header to
// mint a credential usable elsewhere: they would only produce one that is useless.
type RelyingParty struct {
	ID          string // e.g. "docker.example.com"
	Origin      string // e.g. "https://docker.example.com"
	DisplayName string
}

// webauthnAPI builds the library handle for one ceremony.
func (rp RelyingParty) webauthnAPI() (*webauthn.WebAuthn, error) {
	if rp.ID == "" || rp.Origin == "" {
		return nil, ErrPasskeyUnavailable
	}
	name := rp.DisplayName
	if name == "" {
		name = "Docker Commander"
	}
	return webauthn.New(&webauthn.Config{
		RPID:          rp.ID,
		RPDisplayName: name,
		RPOrigins:     []string{rp.Origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// A second factor should prove possession of the device. Asking for user
			// verification as well (a PIN or a fingerprint) would be a stronger claim,
			// but "preferred" is what keeps a plain security key usable.
			//
			// Preferred rather than required for the resident key too. A discoverable
			// credential is what lets the authenticator offer itself before anyone has
			// said who they are, which is the whole of passwordless sign-in — but
			// demanding one would turn "add a passkey" into an error on hardware with
			// no room to store it, and as a second factor it does not need to be
			// discoverable at all. So: ask, use it if we get it, and let the
			// passwordless button simply find nothing on keys that could not.
			UserVerification: protocol.VerificationPreferred,
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
		},
	})
}

// passkeyUser adapts an account to the library's User interface.
//
// The handle is what the authenticator stores beside the key, so it must be the
// account's stable, opaque one — never the username, which the user can see change
// under them and which would leak into the authenticator's own storage.
type passkeyUser struct {
	handle      []byte
	name        string
	credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u passkeyUser) WebAuthnName() string                       { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// ceremonies holds the server half of an in-flight WebAuthn ceremony.
//
// The challenge must be remembered between "begin" and "finish" and must be good
// for exactly one attempt, which is the same shape as the MFA challenge tokens next
// door. Held in memory on purpose: it is worthless after ~2 minutes, and writing it
// to the database would mean a row per abandoned tap of a button.
type ceremonies struct {
	mu   sync.Mutex
	open map[string]ceremony
}

type ceremony struct {
	data    webauthn.SessionData
	expires time.Time
	// stepUp records whether the password was proved to open this ceremony. The
	// account's protection can change while a ceremony is open, and what authorises
	// finishing it is the state it was opened under — see FinishPasskeyRegistration.
	stepUp bool
}

func newCeremonies() *ceremonies {
	return &ceremonies{open: make(map[string]ceremony)}
}

const ceremonyTTL = 2 * time.Minute

// maxOpenCeremonies bounds the map.
//
// Registration and the 2FA step overwrite a key per user or per challenge token and
// both need credentials first, so neither can grow this. Passwordless sign-in can:
// it is reachable by anyone, and every call mints a FRESH random key. At a couple of
// hundred bytes each and a two-minute life, a sustained flood is an out-of-memory
// condition on the small machine this app tends to run on. Refusing new ceremonies
// is a bad minute; being killed is a bad day.
const maxOpenCeremonies = 4096

// put records a ceremony. It reports false when the server is already holding as
// many as it will.
func (c *ceremonies) put(key string, data webauthn.SessionData, stepUp bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep(time.Now())
	// Replacing an existing key is always allowed: it frees as much as it takes, and
	// refusing it would let a flood block someone's own second attempt.
	if _, existing := c.open[key]; !existing && len(c.open) >= maxOpenCeremonies {
		return false
	}
	c.open[key] = ceremony{data: data, expires: time.Now().Add(ceremonyTTL), stepUp: stepUp}
	return true
}

// take returns the ceremony and removes it: one begin, one finish. A challenge
// that survived its answer could be replayed with a captured assertion.
func (c *ceremonies) take(key string) (ceremony, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	found, ok := c.open[key]
	delete(c.open, key)
	if !ok || time.Now().After(found.expires) {
		return ceremony{}, false
	}
	return found, true
}

// sweep drops expired ceremonies. Bounded per call, for the same reason the rate
// limiter's sweep is: this runs on a user-triggered path.
func (c *ceremonies) sweep(now time.Time) {
	const maxScan = 128
	scanned := 0
	for key, cer := range c.open {
		if now.After(cer.expires) {
			delete(c.open, key)
		}
		if scanned++; scanned >= maxScan {
			return
		}
	}
}

// BeginPasskeyRegistration starts pairing a passkey for an account.
//
// stepUp says whether the caller proved the password. It is remembered with the
// ceremony rather than re-derived at the end; see FinishPasskeyRegistration.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, rp RelyingParty, userID int64, stepUp bool) (*protocol.CredentialCreation, error) {
	api, err := rp.webauthnAPI()
	if err != nil {
		return nil, err
	}
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	n, err := s.store.CountFactors(ctx, userID)
	if err != nil {
		return nil, err
	}
	if n >= maxFactorsPerAccount {
		return nil, ErrTooManyFactors
	}
	pu, err := s.passkeyUser(ctx, u)
	if err != nil {
		return nil, err
	}

	// Excluding what is already registered is what makes the browser say "you
	// already have a passkey for this site" instead of silently pairing a second
	// one for the same device.
	exclude := make([]protocol.CredentialDescriptor, 0, len(pu.credentials))
	for _, cred := range pu.credentials {
		exclude = append(exclude, cred.Descriptor())
	}

	creation, session, err := api.BeginRegistration(pu, webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, err
	}
	if !s.ceremonies.put(registrationKey(userID), *session, stepUp) {
		return nil, ErrTooBusy
	}
	return creation, nil
}

// FinishPasskeyRegistration completes pairing and stores the credential.
//
// The password check lives in "begin", but the factor is created here, and the two
// are minutes apart. An account with no second factor may open a ceremony without a
// password — there is nothing to protect yet — so if it gained one in the meantime,
// finishing would add an authenticator to a now-protected account on the strength
// of a session alone. What authorised the ceremony has to still be enough when it
// lands.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, rp RelyingParty, userID int64, name string, r *http.Request) error {
	api, err := rp.webauthnAPI()
	if err != nil {
		return err
	}
	cer, ok := s.ceremonies.take(registrationKey(userID))
	if !ok {
		return ErrInvalidCreds
	}
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	// A cheap early answer, so an unauthorised ceremony is refused before the body is
	// parsed and with an error that says why. It is NOT the guard: the body is read
	// below, which hands the client control of how long this takes. The decision that
	// counts is made inside the insert.
	if u.MFAEnabled && !cer.stepUp {
		return ErrEnrollmentStale
	}
	pu, err := s.passkeyUser(ctx, u)
	if err != nil {
		return err
	}

	credential, err := api.FinishRegistration(pu, cer.data, r)
	if err != nil {
		return ErrInvalidCreds
	}
	blob, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	// Re-check the cap here as well as in Begin: the two are minutes apart, and the
	// account may have filled up in between.
	n, err := s.store.CountFactors(ctx, userID)
	if err != nil {
		return err
	}
	if n >= maxFactorsPerAccount {
		return ErrTooManyFactors
	}
	// cer.stepUp is what authorises this. Passing it down rather than acting on the
	// check above is the point: between that check and here the request body was
	// read, and a client that stalls it decides how long that takes. The store
	// settles "was this account still unprotected?" at the moment of the write.
	_, err = s.store.CreateFactor(ctx, &store.AuthFactor{
		UserID:       userID,
		Kind:         store.FactorKindPasskey,
		Name:         name,
		CredentialID: base64.RawURLEncoding.EncodeToString(credential.ID),
		Credential:   string(blob),
	}, cer.stepUp)
	if errors.Is(err, store.ErrNotFirstFactor) {
		return ErrEnrollmentStale
	}
	return err
}

// BeginPasskeyLogin issues an assertion challenge for the account named by an MFA
// challenge token. The token is proof the password was right; this is the second
// factor.
func (s *Service) BeginPasskeyLogin(ctx context.Context, rp RelyingParty, challengeToken string) (*protocol.CredentialAssertion, error) {
	api, err := rp.webauthnAPI()
	if err != nil {
		return nil, err
	}
	claims, err := s.tokens.Parse(challengeToken)
	if err != nil || claims.Kind != KindMFAChallenge || claims.ID == "" {
		return nil, ErrInvalidCreds
	}
	u, err := s.store.UserByID(ctx, claims.UserID)
	if err != nil {
		return nil, ErrInvalidCreds
	}
	pu, err := s.passkeyUser(ctx, u)
	if err != nil {
		return nil, err
	}
	if len(pu.credentials) == 0 {
		return nil, ErrNoPasskeys
	}

	assertion, session, err := api.BeginLogin(pu)
	if err != nil {
		return nil, err
	}
	// Keyed on the challenge token's own id, so one password entry funds one
	// ceremony — the same rule the TOTP path follows.
	// A login ceremony has no step-up to carry: the password is what minted the
	// challenge token this is keyed on.
	if !s.ceremonies.put(loginKey(claims.ID), *session, true) {
		return nil, ErrTooBusy
	}
	return assertion, nil
}

// FinishPasskeyLogin verifies an assertion and issues a session.
//
// rlKey is the rate-limit bucket, as for VerifyMFA: a passkey is not guessable, but
// the endpoint must not become a free oracle for probing which accounts exist.
func (s *Service) FinishPasskeyLogin(ctx context.Context, rp RelyingParty, rlKey, challengeToken string, r *http.Request, info SessionInfo) (*LoginResult, error) {
	if !s.limiter.Allow(rlKey) {
		return nil, ErrRateLimited
	}
	api, err := rp.webauthnAPI()
	if err != nil {
		return nil, err
	}
	claims, err := s.tokens.Parse(challengeToken)
	if err != nil || claims.Kind != KindMFAChallenge || claims.ID == "" || claims.ExpiresAt == nil {
		s.limiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	userKey := mfaKey(claims.UserID)
	if !s.limiter.Allow(userKey) {
		return nil, ErrRateLimited
	}
	// One challenge, one attempt — spent before the assertion is looked at, exactly
	// as the TOTP path spends it.
	if !s.challenges.spend(claims.ID, claims.ExpiresAt.Time) {
		s.limiter.Fail(rlKey)
		s.limiter.Fail(userKey)
		return nil, ErrInvalidCreds
	}
	cer, ok := s.ceremonies.take(loginKey(claims.ID))
	if !ok {
		s.limiter.Fail(rlKey)
		s.limiter.Fail(userKey)
		return nil, ErrInvalidCreds
	}

	u, err := s.store.UserByID(ctx, claims.UserID)
	if err != nil {
		return nil, ErrInvalidCreds
	}
	pu, err := s.passkeyUser(ctx, u)
	if err != nil {
		return nil, err
	}

	credential, err := api.FinishLogin(pu, cer.data, r)
	if err != nil {
		s.limiter.Fail(rlKey)
		s.limiter.Fail(userKey)
		return nil, ErrInvalidCreds
	}
	// A counter that went backwards means this key answered from two places. The
	// library reports it; refusing is the only useful response, because the honest
	// device and the copy are indistinguishable from here.
	if credential.Authenticator.CloneWarning {
		s.limiter.Fail(rlKey)
		s.limiter.Fail(userKey)
		return nil, ErrClonedAuthenticator
	}

	// Persist the moved counter. A failure here would leave the counter behind and
	// weaken the next login's clone check, so it fails the login rather than
	// quietly succeeding.
	factor, err := s.store.FactorByCredentialID(ctx, base64.RawURLEncoding.EncodeToString(credential.ID))
	if err != nil {
		// The signature already verified, so this is our storage failing, not the
		// user failing a second factor. Saying so keeps a database hiccup out of the
		// audit log as "invalid credentials" and out of the rate limiter.
		return nil, fmt.Errorf("passkey verified but its record could not be read: %w", err)
	}
	if factor.UserID != u.ID {
		// Unreachable while credential ids are unique, which the schema enforces —
		// the library has already refused a credential that is not in this user's
		// own list. Kept as the last line rather than removed: if the uniqueness
		// ever lapses, this is the difference between a wrong login and no login.
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
	s.limiter.Reset(userKey)
	return s.issueSession(ctx, u, info)
}

// passkeyUser loads the account's passkeys in the shape the library wants.
func (s *Service) passkeyUser(ctx context.Context, u *store.User) (passkeyUser, error) {
	handle, err := s.store.WebAuthnHandle(ctx, u.ID)
	if err != nil {
		return passkeyUser{}, err
	}
	factors, err := s.store.ListFactors(ctx, u.ID)
	if err != nil {
		return passkeyUser{}, err
	}
	creds := make([]webauthn.Credential, 0, len(factors))
	for _, f := range factors {
		if f.Kind != store.FactorKindPasskey || f.Credential == "" {
			continue
		}
		var cred webauthn.Credential
		if err := json.Unmarshal([]byte(f.Credential), &cred); err != nil {
			return passkeyUser{}, fmt.Errorf("passkey %d is unreadable: %w", f.ID, err)
		}
		creds = append(creds, cred)
	}
	return passkeyUser{handle: handle, name: u.Username, credentials: creds}, nil
}

// HasPasskeys reports whether the account has any paired.
func (s *Service) HasPasskeys(ctx context.Context, userID int64) (bool, error) {
	factors, err := s.store.ListFactors(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, f := range factors {
		if f.Kind == store.FactorKindPasskey {
			return true, nil
		}
	}
	return false, nil
}

func registrationKey(userID int64) string { return fmt.Sprintf("register:%d", userID) }
func loginKey(challengeID string) string  { return "login:" + challengeID }
