package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// `exp` is optional in a JWT, and jwt.ParseWithClaims is happy without one — so
// a challenge token missing it parses cleanly and then gets dereferenced for the
// spend window. That is a nil pointer on an endpoint reachable before the second
// factor, i.e. a crash an unauthenticated caller could aim at.
//
// Reaching it needs the signing secret, so this is a defence in depth rather than
// an open door. It is also one condition, and the alternative to writing it is a
// panic in the login path.
func TestVerifyMFARejectsAChallengeWithNoExpiry(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := crypto.New(key)
	st.SetCipher(c)

	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	svc := NewService(st, NewTokenManager(secret, time.Hour))

	u, err := svc.Setup(context.Background(), "admin", "correcthorse123")
	if err != nil {
		t.Fatal(err)
	}

	// A challenge token that is valid in every way except that it never expires.
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: u.ID, Username: u.Username, Role: u.Role, Kind: KindMFAChallenge,
		RegisteredClaims: jwt.RegisteredClaims{ID: "no-expiry"},
	}).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}

	// The assertion is as much "does not panic" as it is the error value: without
	// the guard this line takes the process down.
	if _, err := svc.VerifyMFA(context.Background(), "10.0.0.1", tok, "000000", SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
		t.Fatalf("a challenge with no expiry should be refused, got %v", err)
	}
}
