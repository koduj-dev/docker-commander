package store

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/crypto"
)

// TestScanAuthDoesNotLeakTheConnectionWhenCipherIsNil is the regression for a
// bug found while building the embedded reverse proxy (internal/proxy): the
// unrelated code under test there happened to call AuthForHost with no
// cipher configured, and every store query after it hung forever.
//
// scanAuth used to check s.cipher == nil and return BEFORE ever calling
// row.Scan on the *sql.Row from QueryRowContext — but Scan is the only thing
// that releases that row's connection back to the pool (database/sql's own
// documented behavior). With the store's pool capped at one open connection
// (see Open), a single call here with no cipher configured permanently
// checked out that one connection: not just this call failed, every later
// query against the same *Store silently hung forever.
func TestScanAuthDoesNotLeakTheConnectionWhenCipherIsNil(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	// Write a registry WITH a secret while a cipher is configured...
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCipher(c)
	if _, err := s.CreateRegistry(ctx, "docker hub", "docker.io", "user", "hunter2"); err != nil {
		t.Fatal(err)
	}

	// ...then take the cipher away, standing in for scanAuth being reached
	// with none configured. This shouldn't happen in production — main.go
	// always calls SetCipher before any registry-auth code runs — but a
	// lookup that hits it anyway must fail safely, not wedge the whole store.
	s.SetCipher(nil)
	if _, err := s.AuthForHost(ctx, "docker.io"); err == nil {
		t.Error("AuthForHost with no cipher and an encrypted secret should error")
	}

	// The real assertion: a FOLLOW-UP query must still complete promptly.
	// Bounded with a short deadline so the bug (the pool's one connection
	// stuck checked out forever) fails this test in seconds rather than
	// hanging the whole package's test run.
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.ListRegistries(qctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("a query after a nil-cipher AuthForHost call hung — its connection was leaked, never released")
		}
		t.Fatal(err)
	}
}

// TestAuthForHostNotFoundIsNotMaskedByAMissingCipher: no registry configured
// at all is the common case (most installs have none), and must read as
// ErrNotFound even when no cipher happens to be set either — not a generic
// cipher error that has nothing to do with why the lookup actually failed
// (there was never a row to decrypt in the first place).
func TestAuthForHostNotFoundIsNotMaskedByAMissingCipher(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	_, err = s.AuthForHost(ctx, "docker.io")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("no matching registry should be ErrNotFound, got %v", err)
	}
}
