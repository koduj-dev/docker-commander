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
//
// dc_cid names the OAuth client the token was issued to. It exists so a token
// can be revoked before it expires: the verifier requires that client to still
// be registered, which turns "remove this connector" into an immediate
// revocation instead of a promise that comes true within AccessTokenTTL.
type accessClaims struct {
	ReadOnly bool   `json:"dc_ro,omitempty"`
	ClientID string `json:"dc_cid,omitempty"`
	jwt.RegisteredClaims
}

// AccessTokenTTL is the lifetime of an issued access token. Short by design —
// refresh tokens (rotated) cover longer sessions.
const AccessTokenTTL = 15 * time.Minute

// MintAccessToken issues a signed, audience-bound access token for userID,
// bound to the OAuth client it was issued to.
func MintAccessToken(key []byte, issuer, resource string, userID int64, clientID string, readOnly bool, ttl time.Duration) (string, time.Time, error) {
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
		ClientID: clientID,
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
// audience, returning the subject user ID, the client it was issued to, the
// read-only flag and the expiry.
//
// A token minted before dc_cid existed yields an empty clientID. That is
// deliberately not an error: the claim is only ever written by us and a forged
// or stripped one fails the signature, so the sole consequence is that such a
// token isn't revocable by removing its client — and it expires within
// AccessTokenTTL anyway. Rejecting them would force every connector to
// re-authorize on upgrade to buy at most fifteen minutes.
func parseAccessToken(key []byte, resource, tokenStr string) (userID int64, clientID string, readOnly bool, exp time.Time, err error) {
	var c accessClaims
	_, err = jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		return key, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience(resource), // audience binding — rejects tokens for other resources
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, "", false, time.Time{}, err
	}
	uid, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil {
		return 0, "", false, time.Time{}, errors.New("bad subject")
	}
	if c.ExpiresAt == nil {
		return 0, "", false, time.Time{}, errors.New("missing expiry")
	}
	return uid, c.ClientID, c.ReadOnly, c.ExpiresAt.Time, nil
}
