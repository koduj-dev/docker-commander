package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// accessClaims is the payload of an MCP OAuth access token. dc_ro marks a
// read-only grant (the consent screen lets the user choose). Audience binding
// (aud == the canonical /mcp resource URI) is enforced on parse, per the MCP
// auth spec / RFC 8707 — a token issued for another resource is rejected.
type accessClaims struct {
	ReadOnly bool `json:"dc_ro,omitempty"`
	jwt.RegisteredClaims
}

// AccessTokenTTL is the lifetime of an issued access token. Short by design —
// refresh tokens (rotated) cover longer sessions.
const AccessTokenTTL = 15 * time.Minute

// MintAccessToken issues a signed, audience-bound access token for userID.
func MintAccessToken(key []byte, issuer, resource string, userID int64, readOnly bool, ttl time.Duration) (string, time.Time, error) {
	if len(key) == 0 {
		return "", time.Time{}, errors.New("no signing key")
	}
	now := time.Now()
	exp := now.Add(ttl)
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", time.Time{}, err
	}
	claims := accessClaims{
		ReadOnly: readOnly,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   strconv.FormatInt(userID, 10),
			Audience:  jwt.ClaimStrings{resource},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        hex.EncodeToString(jti),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
	return signed, exp, err
}

// parseAccessToken verifies an access token's signature, algorithm, expiry and
// audience, returning the subject user ID, read-only flag and expiry.
func parseAccessToken(key []byte, resource, tokenStr string) (userID int64, readOnly bool, exp time.Time, err error) {
	var c accessClaims
	_, err = jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		return key, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience(resource), // audience binding — rejects tokens for other resources
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, false, time.Time{}, err
	}
	uid, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil {
		return 0, false, time.Time{}, errors.New("bad subject")
	}
	if c.ExpiresAt == nil {
		return 0, false, time.Time{}, errors.New("missing expiry")
	}
	return uid, c.ReadOnly, c.ExpiresAt.Time, nil
}
