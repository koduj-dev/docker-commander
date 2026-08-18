package docker

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// testUnknownHostname is deliberately bogus so it can never collide with a
// real entry in the developer machine's own ~/.ssh/known_hosts, which
// knownHostsCallback reads from the live $HOME.
const testUnknownHostname = "docker-commander-test-host.invalid:22"

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
	err := verifyHostKey(&store.Host{Kind: "ssh"}, testUnknownHostname, key)
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
	if err := verifyHostKey(&store.Host{Kind: "ssh", HostKey: line}, testUnknownHostname, key); err != nil {
		t.Fatalf("pinned matching key should be trusted, got %v", err)
	}
}

func TestVerifyHostKey_PinnedDifferentIsMismatch(t *testing.T) {
	_, pinnedLine := newTestHostKey(t)
	presented, _ := newTestHostKey(t) // a different key than the pinned one

	err := verifyHostKey(&store.Host{Kind: "ssh", HostKey: pinnedLine}, testUnknownHostname, presented)
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want HostKeyMismatchError, got %v", err)
	}
	if mismatch.Fingerprint != ssh.FingerprintSHA256(presented) {
		t.Errorf("mismatch fingerprint should be the presented key's")
	}
}

// PENTEST/regression for CR-003: verifyHostKey must pass the real hostname to
// the known_hosts fallback. Before the fix, the callback was invoked with an
// empty hostname and an empty net.Addr, so it could never match any recorded
// line — a legitimate key sitting in the user's own known_hosts was silently
// treated as unknown.
func TestVerifyHostKey_KnownHostsFallbackMatchesByHostname(t *testing.T) {
	key, line := newTestHostKey(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := "recorded.example " + line + "\n"
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := verifyHostKey(&store.Host{Kind: "ssh"}, "recorded.example:22", key); err != nil {
		t.Fatalf("a key matching the known_hosts entry for this hostname should be trusted, got %v", err)
	}

	// A different hostname must not match the recorded entry.
	err := verifyHostKey(&store.Host{Kind: "ssh"}, testUnknownHostname, key)
	var unknown *HostKeyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("an unrecorded hostname should be unknown, got %v", err)
	}

	// The same host recorded with a different key must be a mismatch, not silently unknown.
	otherKey, _ := newTestHostKey(t)
	err = verifyHostKey(&store.Host{Kind: "ssh"}, "recorded.example:22", otherKey)
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("a different key for a recorded hostname should be a mismatch, got %v", err)
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
