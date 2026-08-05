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
	// ldapAuth is the directory bind, swappable so the provisioning rules below
	// (what a login is allowed to grant) can be tested without a directory.
	// Production always uses LDAPAuthenticate.
	ldapAuth func(cfg store.LDAPConfig, username, password string) (*LDAPResult, error)
}

// NewService wires the auth service together.
func NewService(s *store.Store, tm *TokenManager) *Service {
	return &Service{
		store:      s,
		tokens:     tm,
		limiter:    NewLoginLimiter(5, 15*time.Minute),
		challenges: newUsedChallenges(),
		ldapAuth:   LDAPAuthenticate,
	}
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
	return s.store.BumpSessionEpoch(ctx, userID)
}

// Login verifies username+password. If the account has TOTP enabled it returns
// an MFA challenge token; otherwise a full session token. rlKey is the rate
// limit bucket (typically the client IP). exemptMFA skips the 2FA step (used
// for localhost when the admin has allowed it).
func (s *Service) Login(ctx context.Context, rlKey, username, password string, exemptMFA bool) (*LoginResult, error) {
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
	// even for accounts with TOTP enabled.
	if u.TOTPEnabled && !exemptMFA {
		tok, exp, err := s.tokens.Issue(u.ID, u.Username, u.Role, KindMFAChallenge, u.SessionEpoch)
		if err != nil {
			return nil, err
		}
		// The window is deliberately NOT reset here: the login is not finished,
		// and resetting would hand an attacker who has the password a fresh
		// budget for guessing codes — repeatable every time they re-authenticate.
		return &LoginResult{MFARequired: true, Token: tok, ExpiresAt: exp, User: u}, nil
	}
	s.limiter.Reset(rlKey)
	return s.issueSession(ctx, u)
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
func (s *Service) VerifyMFA(ctx context.Context, rlKey, challengeToken, code string) (*LoginResult, error) {
	if !s.limiter.Allow(rlKey) {
		return nil, ErrRateLimited
	}
	claims, err := s.tokens.Parse(challengeToken)
	if err != nil || claims.Kind != KindMFAChallenge {
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
	return s.issueSession(ctx, u)
}

// consumeTOTP validates a code and burns the time step it came from, so the same
// code cannot be presented twice inside its ~90-second window. A code that is
// valid but not newer than the last one accepted is treated exactly like a wrong
// one — the caller must not be able to tell a replay from a typo.
func (s *Service) consumeTOTP(ctx context.Context, u *store.User, code string) bool {
	counter, ok := MatchTOTP(strings.TrimSpace(code), u.TOTPSecret)
	if !ok || counter <= u.TOTPLastCounter {
		return false
	}
	// A failure to persist the watermark must not hand out a session: it would
	// leave the code replayable, which is the thing being prevented.
	return s.store.SetTOTPLastCounter(ctx, u.ID, counter) == nil
}

// VerifyUserPassword checks a password against the account it belongs to, local
// hash or directory bind, without issuing anything.
//
// Used for step-up on operations a session alone must not authorise. It burns the
// same rate-limit budget as a login: otherwise it is a password oracle that
// answers as fast as you can ask, reachable by anyone holding a session.
func (s *Service) VerifyUserPassword(ctx context.Context, rlKey string, u *store.User, password string) bool {
	if !s.limiter.Allow(rlKey) {
		return false
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
	}
	return ok
}

// mfaKey buckets 2FA attempts per account, alongside the per-IP bucket.
func mfaKey(userID int64) string { return "mfa:" + strconv.FormatInt(userID, 10) }

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
func (s *Service) BeginTOTPEnrollment(ctx context.Context, userID int64) (*Enrollment, error) {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	enr, err := GenerateTOTP(u.Username)
	if err != nil {
		return nil, err
	}
	// Re-pairing: an authenticator already works, so the new secret is held aside
	// until the user proves they can generate codes from it. Overwriting the live
	// secret here would mean that abandoning the flow silently disables 2FA and
	// invalidates the authenticator they still have.
	if u.TOTPEnabled && u.TOTPSecret != "" {
		if err := s.store.SetTOTPPending(ctx, userID, enr.Secret); err != nil {
			return nil, err
		}
		return enr, nil
	}
	// First enrolment: nothing to protect, so the secret goes straight in
	// (disabled until confirmed), exactly as before.
	if err := s.store.SetTOTP(ctx, userID, enr.Secret, false); err != nil {
		return nil, err
	}
	return enr, nil
}

// ConfirmTOTPEnrollment validates the first code and enables 2FA for the user.
func (s *Service) ConfirmTOTPEnrollment(ctx context.Context, userID int64, code string) error {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	// A pending secret means a re-pair: validate against the NEW authenticator and
	// only then promote it, so a wrong code leaves the working one in place.
	if u.TOTPPending != "" {
		if !ValidateTOTP(strings.TrimSpace(code), u.TOTPPending) {
			return ErrInvalidMFACode
		}
		return s.store.PromoteTOTPPending(ctx, userID)
	}
	if u.TOTPSecret == "" {
		return errors.New("auth: no pending enrollment")
	}
	if !ValidateTOTP(strings.TrimSpace(code), u.TOTPSecret) {
		return ErrInvalidMFACode
	}
	return s.store.SetTOTP(ctx, userID, u.TOTPSecret, true)
}

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

func (s *Service) issueSession(ctx context.Context, u *store.User) (*LoginResult, error) {
	tok, exp, err := s.tokens.Issue(u.ID, u.Username, u.Role, KindSession, u.SessionEpoch)
	if err != nil {
		return nil, err
	}
	_ = s.store.TouchLogin(ctx, u.ID)
	return &LoginResult{Token: tok, ExpiresAt: exp, User: u}, nil
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
