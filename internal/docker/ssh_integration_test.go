package docker

import (
	"errors"
	"os"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestSSHHostKeyEndToEnd exercises the real ssh.Dial path against a live sshd.
// Set DC_SSH_INTEGRATION="user@host:port" to run it (skipped otherwise) — host
// key exchange happens before auth, so it works even without valid credentials.
func TestSSHHostKeyEndToEnd(t *testing.T) {
	addr := os.Getenv("DC_SSH_INTEGRATION")
	if addr == "" {
		t.Skip("set DC_SSH_INTEGRATION=user@host:port to run")
	}

	// 1. Probe captures the live host key (used by the trust flow).
	keyLine, fp, err := probeSSHHostKey(&store.Host{Kind: "ssh", Address: addr})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if keyLine == "" || fp == "" {
		t.Fatalf("probe returned empty key/fingerprint")
	}
	t.Logf("probed host key %s", fp)

	// 2. With nothing pinned, connecting must report the host as untrusted —
	//    and the typed error must survive ssh.Dial's opaque wrapping.
	_, err = buildSSHClient(&store.Host{Kind: "ssh", Address: addr})
	var unknown *HostKeyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("untrusted host: want HostKeyUnknownError, got %v", err)
	}
	if unknown.Fingerprint != fp {
		t.Errorf("unknown fingerprint %s != probed %s", unknown.Fingerprint, fp)
	}

	// 3. With the probed key pinned, the SSH handshake is trusted: buildSSHClient
	//    returns a client with no host-key error (a later Docker call would fail
	//    because this sshd has no daemon, but that is past key verification).
	if _, err := buildSSHClient(&store.Host{Kind: "ssh", Address: addr, HostKey: keyLine}); err != nil {
		var hk *HostKeyMismatchError
		var uk *HostKeyUnknownError
		if errors.As(err, &hk) || errors.As(err, &uk) {
			t.Fatalf("pinned-correct key should pass verification, got host-key error: %v", err)
		}
		t.Logf("post-handshake (non-host-key) error as expected: %v", err)
	}

	// 4. A wrong pinned key is a hard mismatch (MITM protection).
	bogus := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err = buildSSHClient(&store.Host{Kind: "ssh", Address: addr, HostKey: bogus})
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("wrong pinned key: want HostKeyMismatchError, got %v", err)
	}
	if mismatch.Fingerprint != fp {
		t.Errorf("mismatch should report the presented key %s, got %s", fp, mismatch.Fingerprint)
	}
}
