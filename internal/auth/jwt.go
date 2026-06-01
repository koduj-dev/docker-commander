package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenKind distinguishes a fully-authenticated session token from the
// short-lived intermediate token issued between the password and 2FA steps.
type TokenKind string

const (
	// KindSession is a fully authenticated token (password + 2FA satisfied).
	KindSession TokenKind = "session"
	// KindMFAChallenge is issued after a correct password when TOTP is still
	// required. It only authorises calling the 2FA verification endpoint.
	KindMFAChallenge TokenKind = "mfa"
)

// Claims is the JWT payload used for both session and MFA-challenge tokens.
type Claims struct {
	UserID   int64     `json:"uid"`
	Username string    `json:"usr"`
	Role     string    `json:"role"`
	Kind     TokenKind `json:"knd"`
	jwt.RegisteredClaims
}

// TokenManager mints and verifies HMAC-signed JWTs.
type TokenManager struct {
	secret      []byte
	sessionTTL  time.Duration
	challengeTTL time.Duration
}

// NewTokenManager returns a manager signing with secret. sessionTTL controls
// how long a logged-in session stays valid before re-authentication.
func NewTokenManager(secret []byte, sessionTTL time.Duration) *TokenManager {
	return &TokenManager{
		secret:       secret,
		sessionTTL:   sessionTTL,
		challengeTTL: 5 * time.Minute,
	}
}

// Issue creates a signed token for the given user and kind.
func (m *TokenManager) Issue(userID int64, username, role string, kind TokenKind) (string, time.Time, error) {
	ttl := m.sessionTTL
	if kind == KindMFAChallenge {
		ttl = m.challengeTTL
	}
	now := time.Now()
	exp := now.Add(ttl)
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Kind:     kind,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	return signed, exp, err
}

// Parse validates the signature and expiry and returns the claims.
func (m *TokenManager) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}
