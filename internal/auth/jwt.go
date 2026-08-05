package auth

import (
	"crypto/rand"
	"encoding/hex"
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
	// Epoch is the account's session generation when this token was minted. The
	// middleware refuses a token whose epoch is behind the account's current one,
	// which is how a password change takes effect immediately instead of waiting
	// out the TTL.
	Epoch int64 `json:"ep,omitempty"`
	jwt.RegisteredClaims
}

// TokenManager mints and verifies HMAC-signed JWTs.
type TokenManager struct {
	secret       []byte
	sessionTTL   time.Duration
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

// Issued is a freshly minted token and the facts about it a caller needs: its id
// (for the session row, or for spending a challenge) and when it expires.
type Issued struct {
	Token     string
	ID        string
	ExpiresAt time.Time
}

// Issue creates a signed token for the given user and kind.
func (m *TokenManager) Issue(userID int64, username, role string, kind TokenKind, epoch int64) (Issued, error) {
	ttl := m.sessionTTL
	if kind == KindMFAChallenge {
		ttl = m.challengeTTL
	}
	now := time.Now()
	exp := now.Add(ttl)
	// Both kinds carry a unique id now: a challenge is spendable once and needs
	// something to mark as spent, and a session needs something the owner can
	// point at to say "not that one, this one".
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Issued{}, err
	}
	id := hex.EncodeToString(b)
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Kind:     kind,
		Epoch:    epoch,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        id,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		return Issued{}, err
	}
	return Issued{Token: signed, ID: id, ExpiresAt: exp}, nil
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
