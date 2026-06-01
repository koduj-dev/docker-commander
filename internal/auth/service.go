package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Common authentication errors surfaced to the API layer.
var (
	ErrSetupDone        = errors.New("auth: setup already completed")
	ErrInvalidCreds     = errors.New("auth: invalid credentials")
	ErrRateLimited      = errors.New("auth: too many attempts, try again later")
	ErrMFARequired      = errors.New("auth: 2fa code required")
	ErrInvalidMFACode   = errors.New("auth: invalid 2fa code")
	ErrWeakPassword     = errors.New("auth: password must be at least 10 characters")
	ErrInvalidUsername  = errors.New("auth: username must be 3-32 characters")
)

// Service orchestrates the authentication flows on top of the store and the
// crypto/token primitives in this package.
type Service struct {
	store   *store.Store
	tokens  *TokenManager
	limiter *LoginLimiter
}

// NewService wires the auth service together.
func NewService(s *store.Store, tm *TokenManager) *Service {
	return &Service{
		store:   s,
		tokens:  tm,
		limiter: NewLoginLimiter(5, 15*time.Minute),
	}
}

// LoginResult is returned from Login: either a finished session, or an MFA
// challenge the caller must satisfy via VerifyMFA.
type LoginResult struct {
	MFARequired bool
	Token       string    // session token, or MFA-challenge token if MFARequired
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
	u := &store.User{Username: username, PasswordHash: hash, Role: "admin"}
	id, err := s.store.CreateUser(ctx, u)
	if err != nil {
		return nil, err
	}
	u.ID = id
	return u, nil
}

// Login verifies username+password. If the account has TOTP enabled it returns
// an MFA challenge token; otherwise a full session token. rlKey is the rate
// limit bucket (typically the client IP).
func (s *Service) Login(ctx context.Context, rlKey, username, password string) (*LoginResult, error) {
	if !s.limiter.Allow(rlKey) {
		return nil, ErrRateLimited
	}
	u, err := s.store.UserByUsername(ctx, username)
	if err != nil {
		// Run a dummy hash verification to keep timing roughly constant
		// regardless of whether the username exists.
		_, _ = VerifyPassword(password, dummyHash)
		s.limiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		s.limiter.Fail(rlKey)
		return nil, ErrInvalidCreds
	}
	s.limiter.Reset(rlKey)

	if u.TOTPEnabled {
		tok, exp, err := s.tokens.Issue(u.ID, u.Username, u.Role, KindMFAChallenge)
		if err != nil {
			return nil, err
		}
		return &LoginResult{MFARequired: true, Token: tok, ExpiresAt: exp, User: u}, nil
	}
	return s.issueSession(ctx, u)
}

// VerifyMFA completes login by validating a TOTP code against the MFA-challenge
// token issued by Login.
func (s *Service) VerifyMFA(ctx context.Context, challengeToken, code string) (*LoginResult, error) {
	claims, err := s.tokens.Parse(challengeToken)
	if err != nil || claims.Kind != KindMFAChallenge {
		return nil, ErrInvalidCreds
	}
	u, err := s.store.UserByID(ctx, claims.UserID)
	if err != nil {
		return nil, ErrInvalidCreds
	}
	if !u.TOTPEnabled || !ValidateTOTP(strings.TrimSpace(code), u.TOTPSecret) {
		return nil, ErrInvalidMFACode
	}
	return s.issueSession(ctx, u)
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
	if u.TOTPSecret == "" {
		return errors.New("auth: no pending enrollment")
	}
	if !ValidateTOTP(strings.TrimSpace(code), u.TOTPSecret) {
		return ErrInvalidMFACode
	}
	return s.store.SetTOTP(ctx, userID, u.TOTPSecret, true)
}

func (s *Service) issueSession(ctx context.Context, u *store.User) (*LoginResult, error) {
	tok, exp, err := s.tokens.Issue(u.ID, u.Username, u.Role, KindSession)
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
