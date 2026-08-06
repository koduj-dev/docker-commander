package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Common authentication errors surfaced to the API layer.
var (
	ErrSetupDone       = errors.New("auth: setup already completed")
	ErrInvalidCreds    = errors.New("auth: invalid credentials")
	ErrRateLimited     = errors.New("auth: too many attempts, try again later")
	ErrMFARequired     = errors.New("auth: 2fa code required")
	ErrInvalidMFACode  = errors.New("auth: invalid 2fa code")
	ErrWeakPassword    = errors.New("auth: password must be at least 10 characters")
	ErrInvalidUsername = errors.New("auth: username must be 3-32 characters")
)

// Service orchestrates the authentication flows on top of the store and the
// crypto/token primitives in this package.
type Service struct {
	store   *store.Store
	tokens  *TokenManager
	limiter *LoginLimiter
	// challenges makes an MFA challenge token good for one attempt.
	challenges *usedChallenges
	// ceremonies holds the server half of an in-flight WebAuthn exchange.
	ceremonies *ceremonies
	// publicCeremonies holds the ones anyone can start: passwordless sign-in.
	publicCeremonies *ceremonies
	// passkeyLimiter meters the passwordless endpoints. Separate from limiter
	// because a passkey assertion is not a guess at a password: charging one to the
	// other lets a stranger — or an honest user with a stale key — close the
	// password form for everyone behind a shared address.
	passkeyLimiter *LoginLimiter
	// ldapAuth is the directory bind, swappable so the provisioning rules below
	// (what a login is allowed to grant) can be tested without a directory.
	// Production always uses LDAPAuthenticate.
	ldapAuth func(cfg store.LDAPConfig, username, password string) (*LDAPResult, error)
}

// NewService wires the auth service together.
func NewService(s *store.Store, tm *TokenManager) *Service {
	return &Service{
		store:   s,
		tokens:  tm,
		limiter: NewLoginLimiter(5, 15*time.Minute),
		// A separate, UI-sized budget for starting a passwordless sign-in. Five per
		// fifteen minutes is a password-GUESSING budget; this meters a button whose
		// commonest outcome is the browser prompt being dismissed, and where a
		// success costs a slot too.
		passkeyLimiter: NewLoginLimiter(passkeyAttempts, 5*time.Minute),
		challenges:     newUsedChallenges(),
		ceremonies:     newCeremonies(maxOpenCeremonies),
		// Reachable without credentials, so it gets its own bounded store; see the
		// comment on maxPublicCeremonies.
		publicCeremonies: newCeremonies(maxPublicCeremonies),
		ldapAuth:         LDAPAuthenticate,
	}
}

// passkeyAttempts is how many passwordless starts or attempts one address gets per
// window. Sized for a button rather than for guessing: dismissing the browser
// prompt is the commonest outcome and costs a slot, as does a success.
const passkeyAttempts = 30

// SessionInfo is what a login knows about the client asking for one. Recorded on
// the session so its owner can recognise it later.
type SessionInfo struct {
	IP        string
	UserAgent string
}

// LoginResult is returned from Login: either a finished session, or an MFA
// challenge the caller must satisfy via VerifyMFA.
type LoginResult struct {
	MFARequired bool
	Token       string // session token, or MFA-challenge token if MFARequired
	ExpiresAt   time.Time
	User        *store.User
}

// NeedsSetup reports whether no account exists yet (first-run wizard).
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.store.CountUsers(ctx)
	return n == 0, err
}

// Setup creates the first admin account. It fails once any user exists.
func (s *Service) Setup(ctx context.Context, username, password string) (*store.User, error) {
	needs, err := s.NeedsSetup(ctx)
	if err != nil {
		return nil, err
	}
	if !needs {
		return nil, ErrSetupDone
	}
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	if len(password) < 10 {
		return nil, ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	// The count above is advisory — it makes the common case fail with a clear
	// error — but the decision is made by the insert, which refuses to run once
	// any account exists. Two concurrent setups would otherwise both pass the
	// count and both create an admin.
	u := &store.User{Username: username, PasswordHash: hash, Role: "admin"}
	id, err := s.store.CreateFirstUser(ctx, u)
	if errors.Is(err, store.ErrSetupTaken) {
		return nil, ErrSetupDone
	}
	if err != nil {
		return nil, err
	}
	u.ID = id
	return u, nil
}

// CreateAccount creates a non-setup user account (used by admins). role is
// "admin" or "user"; for "user", sections and readOnly scope their access.
func (s *Service) CreateAccount(ctx context.Context, username, password, role string, readOnly bool, sections []string) (*store.User, error) {
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	if len(password) < 10 {
		return nil, ErrWeakPassword
	}
	if existing, _ := s.store.UserByUsername(ctx, username); existing != nil {
		return nil, errors.New("auth: username already taken")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	if role != "admin" {
		role = "user"
	}
	u := &store.User{Username: username, PasswordHash: hash, Role: role, ReadOnly: readOnly, Sections: sections}
	id, err := s.store.CreateUser(ctx, u)
	if err != nil {
		return nil, err
	}
	u.ID = id
	return u, nil
}

// SetPassword replaces a user's password (admin reset or self-change) and
// invalidates every session already issued for that account.
//
// Without the second half the first is half a control: a JWT is self-contained,
// so nothing about changing the password reaches the copy an attacker already
// holds. They would keep full access for the rest of the token's twelve hours —
// granted by the very act meant to take it away.
func (s *Service) SetPassword(ctx context.Context, userID int64, password string) error {
	if len(password) < 10 {
		return ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := s.store.UpdatePassword(ctx, userID, hash); err != nil {
		return err
	}
	// Both: the epoch voids anything already issued (including tokens whose row
	// is gone), and dropping the rows keeps the session list honest.
	if err := s.store.DeleteUserSessions(ctx, userID); err != nil {
		return err
	}
	return s.store.BumpSessionEpoch(ctx, userID)
}

// Login verifies username+password. If the account has TOTP enabled it returns
// an MFA challenge token; otherwise a full session token. rlKey is the rate
// limit bucket (typically the client IP). exemptMFA skips the 2FA step (used
// for localhost when the admin has allowed it).
func (s *Service) Login(ctx context.Context, rlKey, username, password string, exemptMFA bool, info SessionInfo) (*LoginResult, error) {
	if !s.limiter.Allow(rlKey) {
		return nil, ErrRateLimited
	}
	existing, _ := s.store.UserByUsername(ctx, username)

	var u *store.User
	if existing != nil && existing.AuthSource != "ldap" {
		// Local account: verify the stored password hash.
		ok, err := VerifyPassword(password, existing.PasswordHash)
		if err != nil || !ok {
			s.limiter.Fail(rlKey)
			return nil, ErrInvalidCreds
		}
		u = existing
	} else {
		// LDAP path: either a known LDAP-provisioned account or an unknown user
		// while LDAP is enabled.
		authed, err := s.ldapLogin(ctx, existing, username, password)
		if err != nil {
			s.limiter.Fail(rlKey)
			return nil, ErrInvalidCreds
		}
		u = authed
	}
	// exemptMFA (localhost + admin setting) issues a session straight away,
	// even for accounts with a second factor.
	//
	// The test is "has ANY factor", not "has an authenticator app": an account
	// holding only a passkey is protected by it, and gating on TOTP alone would
	// hand out a session on the password by itself.
	if u.MFAEnabled && !exemptMFA {
		iss, err := s.tokens.Issue(u.ID, u.Username, u.Role, KindMFAChallenge, u.SessionEpoch)
		if err != nil {
			return nil, err
		}
		// The window is deliberately NOT reset here: the login is not finished,
		// and resetting would hand an attacker who has the password a fresh
		// budget for guessing codes — repeatable every time they re-authenticate.
		return &LoginResult{MFARequired: true, Token: iss.Token, ExpiresAt: iss.ExpiresAt, User: u}, nil
	}
	s.limiter.Reset(rlKey)
	return s.issueSession(ctx, u, info)
}

// VerifyMFA completes login by validating a TOTP code against the MFA-challenge
// token issued by Login. rlKey is the same bucket Login uses (the client IP).
//
// This step is rate limited for the same reason the password step is, and the
// reason is easy to miss: by the time it runs the attacker already has the
// password, so a six-digit code is the only thing left. Unthrottled, that is
// ~10^6 guesses — minutes of scripted requests — and the code path is cheap
// (no argon2), which is what makes it practical rather than theoretical.
//
// It is keyed on the client IP *and* on the account, so rotating source
// addresses doesn't buy a fresh budget: the account bucket keeps counting.
func (s *Service) VerifyMFA(ctx context.Context, rlKey, challengeToken, code string, info SessionInfo) (*LoginResult, error) {
	if !s.limiter.Allow(rlKey) {
		return nil, ErrRateLimited
	}
	claims, err := s.tokens.Parse(challengeToken)
	// The expiry is required, not merely honoured: it is what bounds how long a
	// spent id has to be remembered, and `exp` is optional in a JWT, so a token
	// without one would otherwise be dereferenced here — a nil pointer reachable
	// from an unauthenticated endpoint.
	if err != nil || claims.Kind != KindMFAChallenge || claims.ExpiresAt == nil {
		s.limiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	userKey := mfaKey(claims.UserID)
	if !s.limiter.Allow(userKey) {
		return nil, ErrRateLimited
	}
	// One challenge, one attempt — spent before the code is even looked at, so a
	// wrong guess costs another password round trip rather than another try. The
	// rate limiter bounds guesses per window; this bounds them per password entry,
	// which is the half an attacker with the password can otherwise repeat freely.
	if !s.challenges.spend(claims.ID, claims.ExpiresAt.Time) {
		s.limiter.Fail(rlKey)
		s.limiter.Fail(userKey)
		return nil, ErrInvalidCreds
	}
	u, err := s.store.UserByID(ctx, claims.UserID)
	if err != nil {
		s.limiter.Fail(rlKey)
		s.limiter.Fail(userKey)
		return nil, ErrInvalidCreds
	}
	if !u.TOTPEnabled || !s.consumeTOTP(ctx, u, code) {
		s.limiter.Fail(rlKey)
		s.limiter.Fail(userKey)
		return nil, ErrInvalidMFACode
	}
	s.limiter.Reset(rlKey)
	s.limiter.Reset(userKey)
	return s.issueSession(ctx, u, info)
}

// consumeTOTP validates a code against any of the account's paired authenticators
// and burns the time step it came from, so the same code cannot be presented twice
// inside its ~90-second window. A code that is valid but not newer than the last
// one accepted by THAT authenticator is treated exactly like a wrong one — the
// caller must not be able to tell a replay from a typo.
//
// The loop keeps going after a match only because stopping early would complicate
// the code, not because it equalises timing — MatchTOTP returns as soon as a step
// matches, so it does not. Nothing observable leaks either way: the answer is in
// the response.
func (s *Service) consumeTOTP(ctx context.Context, u *store.User, code string) bool {
	factors, err := s.store.ListFactors(ctx, u.ID)
	if err != nil {
		return false
	}
	code = strings.TrimSpace(code)
	var matched *store.AuthFactor
	var matchedCounter int64
	for i := range factors {
		if factors[i].Kind != store.FactorKindTOTP {
			continue
		}
		counter, ok := MatchTOTP(code, factors[i].Secret)
		if ok && counter > factors[i].LastCounter && matched == nil {
			matched = &factors[i]
			matchedCounter = counter
		}
	}
	if matched == nil {
		return false
	}
	// A failure to persist the watermark must not hand out a session: it would
	// leave the code replayable, which is the thing being prevented. That includes
	// losing the race to another request presenting the SAME code — the store
	// reports that as an error precisely so both callers cannot be told yes.
	return s.store.BurnFactorCounter(ctx, matched.ID, matchedCounter) == nil
}

// ListFactors returns the account's paired factors.
func (s *Service) ListFactors(ctx context.Context, userID int64) ([]store.AuthFactor, error) {
	return s.store.ListFactors(ctx, userID)
}

// ErrLastFactor is returned when removing a factor would leave the account with
// no second factor at all.
var ErrLastFactor = store.ErrLastFactor

// RemoveFactor unpairs one of the account's factors.
//
// The last one cannot go. 2FA is mandatory here (bar the localhost exemption, which
// is a property of where you connect from, not of the account), so an account with
// zero factors is one that cannot sign in from anywhere else — a self-lockout with
// no admin reset behind it. Pair the replacement first, then remove the old one.
func (s *Service) RemoveFactor(ctx context.Context, userID, factorID int64) error {
	// The count and the delete are one statement in the store. Doing it here —
	// count, decide, delete — is a race that two concurrent removals win together:
	// both see two factors, both delete, and the account is left with NONE. Which
	// is not a lockout: 2FA is derived from whether any factor exists, so zero
	// factors means the password alone signs in, from anywhere. The guard exists to
	// forbid exactly that, so it has to be atomic.
	return s.store.DeleteFactor(ctx, factorID, userID)
}

// VerifyUserPassword checks a password against the account it belongs to, local
// hash or directory bind, without issuing anything. It returns nil when the
// password is right, ErrRateLimited when the budget for this key is spent, and
// ErrInvalidCreds otherwise.
//
// Used for step-up on operations a session alone must not authorise. It burns a
// rate-limit budget: otherwise it is a password oracle that answers as fast as you
// can ask, reachable by anyone holding a session.
//
// Telling the two failures apart matters. Reporting a spent budget as "wrong
// password" tells the owner their own password is wrong, which is both false and
// the exact moment they are trying to recover an account.
func (s *Service) VerifyUserPassword(ctx context.Context, rlKey string, u *store.User, password string) error {
	if !s.limiter.Allow(rlKey) {
		return ErrRateLimited
	}
	ok := false
	if u.AuthSource == "ldap" {
		_, err := s.ldapLogin(ctx, u, u.Username, password)
		ok = err == nil
	} else {
		verified, err := VerifyPassword(password, u.PasswordHash)
		ok = err == nil && verified
	}
	if !ok {
		s.limiter.Fail(rlKey)
		return ErrInvalidCreds
	}
	// Reset on success, exactly as Login does. Without it, someone else's failed
	// attempts from the same address keep the real account holder locked out of
	// their own step-up until the window rolls over — a denial of service bought
	// with wrong guesses.
	s.limiter.Reset(rlKey)
	return nil
}

// mfaKey buckets 2FA attempts per account, alongside the per-IP bucket.
func mfaKey(userID int64) string { return "mfa:" + strconv.FormatInt(userID, 10) }

// StepUpKey buckets password re-checks per account AND per session.
//
// Not the client address, which is what login uses: keying it there let anyone
// holding a session spend the address's login budget — five wrong passwords on
// "remove this authenticator" and nobody at that address could sign in for fifteen
// minutes.
//
// But not the account alone either, which merely moves the damage onto the victim.
// Step-up is reachable with nothing but a session, so a stolen one could burn the
// account's whole budget every fifteen minutes: the owner's CORRECT password is
// then refused for exactly the two things they need to recover — removing the
// attacker's authenticator and pairing a replacement — while logins keep working,
// so nothing looks broken.
//
// Per session, the attacker's stolen session spends its own budget and the owner's
// is untouched. Minting more sessions needs the password, which is what the
// attacker is trying to guess.
func StepUpKey(userID int64, sessionID string) string {
	return "stepup:" + strconv.FormatInt(userID, 10) + ":" + sessionID
}

// ChallengeUsername reports which account an MFA challenge token names, so a
// failed verification can be audited against it. It validates the token's
// signature and kind but says nothing about the code — callers must not treat a
// non-empty result as authentication.
func (s *Service) ChallengeUsername(challengeToken string) string {
	claims, err := s.tokens.Parse(challengeToken)
	if err != nil || claims.Kind != KindMFAChallenge {
		return ""
	}
	return claims.Username
}

// BeginTOTPEnrollment generates a new secret + QR for the user. The secret is
// stored but not yet enabled until confirmed via ConfirmTOTPEnrollment.
//
// stepUp says whether the caller proved the password to get here. It is stored
// with the candidate so that confirming it can be judged against the account's
// protection at the time the enrolment began — see ConfirmTOTPEnrollment.
func (s *Service) BeginTOTPEnrollment(ctx context.Context, userID int64, stepUp bool) (*Enrollment, error) {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	enr, err := GenerateTOTP(u.Username)
	if err != nil {
		return nil, err
	}
	// The candidate is always held aside until the user proves they can generate
	// codes from it, and only then becomes a factor. Nothing that already works is
	// touched, so abandoning the flow changes nothing — which is what makes "add
	// another authenticator" safe to click.
	if err := s.store.SetTOTPPending(ctx, userID, enr.Secret, stepUp); err != nil {
		return nil, err
	}
	return enr, nil
}

// ConfirmTOTPEnrollment validates the first code from the candidate authenticator
// and pairs it. Anything already paired keeps working: this adds a factor, it does
// not replace one.
func (s *Service) ConfirmTOTPEnrollment(ctx context.Context, userID int64, code, name string) error {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.TOTPPending == "" {
		return errors.New("auth: no pending enrollment")
	}
	// The password is what authorises adding a factor to an account that already has
	// one, and this is the moment the factor is actually created — so this is where
	// that has to hold. Checking it only when the enrolment starts leaves the
	// candidate redeemable across a change in the account's protection: a stolen
	// session on an account with no second factor can begin an enrolment (rightly,
	// nothing to protect), sit on the secret while the owner pairs a passkey, and
	// confirm it afterwards against an account that is now protected.
	if u.MFAEnabled && !u.TOTPPendingStepUp {
		return ErrEnrollmentStale
	}
	if !ValidateTOTP(strings.TrimSpace(code), u.TOTPPending) {
		return ErrInvalidMFACode
	}
	// Bound how many an account may hold. Adding one needs the password, so this is
	// hygiene rather than a gate — but every verification walks the list, and there
	// is no reason for it to be unbounded.
	n, err := s.store.CountFactors(ctx, userID)
	if err != nil {
		return err
	}
	if n >= maxFactorsPerAccount {
		return ErrTooManyFactors
	}
	// Claiming the pending secret and inserting the factor happen together, in one
	// transaction, keyed on the secret still being there. Reading it, validating a
	// code and then inserting is a race that sixteen parallel POSTs win together:
	// every one of them passes the check and inserts, and one enrolment becomes N
	// factors holding ONE secret. The watermark is per factor, so each future code
	// from that authenticator would then be spendable N times.
	if _, err := s.store.PairPendingFactor(ctx, userID, u.TOTPPending, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Someone else already completed this enrolment.
			return ErrInvalidMFACode
		}
		return err
	}
	return nil
}

// maxFactorsPerAccount bounds the list. Ten devices is far past anything real.
const maxFactorsPerAccount = 10

// ErrTooManyFactors is returned when an account already holds the maximum.
var ErrTooManyFactors = errors.New("auth: this account already has the maximum number of authenticators")

// ErrEnrollmentStale is returned when a pending enrolment was started before the
// account had any second factor and so was never authorised with the password,
// but the account has gained one since. Starting over asks for the password.
var ErrEnrollmentStale = errors.New("auth: this account is protected now — start again and confirm with your password")

// ldapLogin authenticates against LDAP and provisions a local account on first
// login (so roles/sections persist). A dummy password verification keeps timing
// roughly constant when LDAP is off and the user is unknown.
func (s *Service) ldapLogin(ctx context.Context, existing *store.User, username, password string) (*store.User, error) {
	cfg, err := s.store.GetLDAP(ctx)
	if err != nil || !cfg.Configured() {
		_, _ = VerifyPassword(password, dummyHash)
		return nil, ErrInvalidCreds
	}
	res, err := s.ldapAuth(cfg, username, password)
	if err != nil {
		return nil, ErrInvalidCreds
	}

	// When group→section mappings are configured, LDAP is authoritative for a
	// user's sections: derive them from current group membership.
	groupMapped := len(cfg.GroupMappings) > 0
	mappedSections := SectionsForGroups(cfg, res.Groups)
	// Roles are authoritative only once at least one mapping actually hands one
	// out. A config written before group→role mapping existed grants sections
	// only, and must not silently strip roles an admin assigned by hand.
	roleMapped := MapsRoles(cfg)
	mappedRoles := RolesForGroups(cfg, res.Groups)

	if existing != nil {
		// Re-sync sections from LDAP groups on each login so membership changes
		// take effect. Role and read-only stay as set locally (admin is sticky to
		// avoid lockout if the directory is briefly unreachable or misconfigured).
		if groupMapped && !sameSections(existing.Sections, mappedSections) {
			if err := s.store.UpdateUserAccess(ctx, existing.ID, existing.Role, existing.ReadOnly, mappedSections); err != nil {
				return nil, err
			}
			existing.Sections = mappedSections
		}
		if roleMapped {
			if err := s.syncRoles(ctx, existing.ID, mappedRoles, cfg.FallbackRoleID); err != nil {
				return nil, err
			}
		}
		// Keep the alert address in step with the directory when it publishes one.
		// A blank mail attribute never clears an address the user set by hand —
		// losing it silently would stop their alerts arriving.
		if res.Email != "" && res.Email != existing.Email {
			if err := s.store.SetUserEmail(ctx, existing.ID, res.Email); err != nil {
				return nil, err
			}
			existing.Email = res.Email
		}
		return existing, nil
	}
	role := "user"
	if res.IsAdmin {
		role = "admin"
	}
	u := &store.User{Username: res.Username, Role: role, AuthSource: "ldap", Sections: mappedSections, Email: res.Email}
	id, err := s.store.CreateUser(ctx, u)
	if err != nil {
		return nil, err
	}
	u.ID = id
	if roleMapped {
		if err := s.syncRoles(ctx, u.ID, mappedRoles, cfg.FallbackRoleID); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// syncRoles replaces a user's roles with the ones their LDAP groups grant,
// skipping the write when nothing changed so an unchanged login doesn't churn
// the table.
//
// A mapping can name a role that has since been deleted. That must not fail the
// login — deleting a role would then lock out every member of the groups naming
// it — so the id is dropped. Dropping it silently leaves those users with
// nothing, which is why an admin can nominate a fallback role: it stands in for
// whatever failed to resolve, degrading them to a known baseline (Viewer, say)
// rather than to no access at all.
//
// The fallback deliberately does NOT apply to a user whose groups map to no role
// at all. That is the ordinary "not entitled" case, and granting a baseline there
// would hand access to every account in the directory that can authenticate.
func (s *Service) syncRoles(ctx context.Context, userID int64, want []int64, fallback int64) error {
	valid, err := s.store.ExistingRoleIDs(ctx, want)
	if err != nil {
		return err
	}
	if len(valid) < len(want) && fallback > 0 {
		fb, err := s.store.ExistingRoleIDs(ctx, []int64{fallback})
		if err != nil {
			return err
		}
		if len(fb) == 1 && !containsID(valid, fallback) {
			valid = append(valid, fallback)
		}
	}
	have, err := s.store.RoleIDsForUser(ctx, userID)
	if err != nil {
		return err
	}
	if sameIDs(have, valid) {
		return nil
	}
	return s.store.SetUserRoles(ctx, userID, valid)
}

// containsID reports whether ids holds id.
func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// sameSections reports whether two section lists hold the same set (order
// independent), so we skip a needless write when membership hasn't changed.
func sameSections(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}

// sameIDs reports whether two id lists hold the same set, order independent.
func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[int64]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

func (s *Service) issueSession(ctx context.Context, u *store.User, info SessionInfo) (*LoginResult, error) {
	iss, err := s.tokens.Issue(u.ID, u.Username, u.Role, KindSession, u.SessionEpoch)
	if err != nil {
		return nil, err
	}
	// Sessions are only ever added here, so this is the one place that will
	// notice the table growing. Expired rows can no longer authenticate anything
	// — they would just be clutter in someone's profile list.
	_ = s.store.PurgeExpiredSessions(ctx)
	// The row IS the session: the middleware refuses a token with no matching one,
	// so failing to record it must fail the login rather than hand out a token
	// nothing can revoke.
	if err := s.store.CreateSession(ctx, &store.Session{
		ID: iss.ID, UserID: u.ID, IP: info.IP, UserAgent: info.UserAgent, ExpiresAt: iss.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	_ = s.store.TouchLogin(ctx, u.ID)
	return &LoginResult{Token: iss.Token, ExpiresAt: iss.ExpiresAt, User: u}, nil
}

func validateUsername(u string) error {
	if n := len(strings.TrimSpace(u)); n < 3 || n > 32 {
		return ErrInvalidUsername
	}
	return nil
}

// dummyHash is a precomputed Argon2id hash used to equalize login timing for
// nonexistent usernames. Its plaintext is irrelevant.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$YWJjZGVmZ2hpamtsbW5vcA$3hAheBQHKO0Cj0r8e3kEErZsZTo7on3Chj0Htg4Ll0g"
