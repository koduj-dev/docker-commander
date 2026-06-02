package docker

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// newTestHostKey generates a throwaway ed25519 SSH key and returns its public
// key plus the authorized_keys line used to pin it.
func newTestHostKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	_ = pub
	pk := signer.PublicKey()
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	return pk, line
}

func TestVerifyHostKey_UnknownWhenNothingPinned(t *testing.T) {
	key, _ := newTestHostKey(t)
	// No pinned key, and we rely on known_hosts not containing a random key.
	err := verifyHostKey(&store.Host{Kind: "ssh"}, key)
	var unknown *HostKeyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("want HostKeyUnknownError, got %v", err)
	}
	if unknown.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Errorf("fingerprint mismatch: %s vs %s", unknown.Fingerprint, ssh.FingerprintSHA256(key))
	}
}

func TestVerifyHostKey_PinnedMatchAccepts(t *testing.T) {
	key, line := newTestHostKey(t)
	if err := verifyHostKey(&store.Host{Kind: "ssh", HostKey: line}, key); err != nil {
		t.Fatalf("pinned matching key should be trusted, got %v", err)
	}
}

func TestVerifyHostKey_PinnedDifferentIsMismatch(t *testing.T) {
	_, pinnedLine := newTestHostKey(t)
	presented, _ := newTestHostKey(t) // a different key than the pinned one

	err := verifyHostKey(&store.Host{Kind: "ssh", HostKey: pinnedLine}, presented)
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want HostKeyMismatchError, got %v", err)
	}
	if mismatch.Fingerprint != ssh.FingerprintSHA256(presented) {
		t.Errorf("mismatch fingerprint should be the presented key's")
	}
}

func TestKeysEqual(t *testing.T) {
	a, _ := newTestHostKey(t)
	b, _ := newTestHostKey(t)
	if !keysEqual(a, a) {
		t.Error("a key should equal itself")
	}
	if keysEqual(a, b) {
		t.Error("distinct keys should not be equal")
	}
	if keysEqual(a, nil) || keysEqual(nil, a) || keysEqual(nil, nil) {
		t.Error("nil keys should never be equal")
	}
}
