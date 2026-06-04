package docker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// remoteDockerSocket is where the daemon listens on the remote host.
const remoteDockerSocket = "/var/run/docker.sock"

// HostKeyUnknownError is returned on first contact with an SSH host whose key
// is neither pinned in the DB nor present in ~/.ssh/known_hosts. The caller can
// surface the fingerprint to the operator and, on explicit approval, pin it.
type HostKeyUnknownError struct {
	Fingerprint string // SHA256:… of the presented key
	KeyType     string // e.g. "ssh-ed25519"
}

func (e *HostKeyUnknownError) Error() string {
	return fmt.Sprintf("host key not trusted (%s %s) — verify and trust it before connecting", e.KeyType, e.Fingerprint)
}

// HostKeyMismatchError is returned when the presented key differs from the one
// we already trust. This is a hard stop: it can mean a reinstalled host — or a
// man-in-the-middle. We never auto-accept; the operator must re-trust manually.
type HostKeyMismatchError struct {
	Fingerprint string // SHA256:… of the key actually presented now
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("REMOTE HOST KEY CHANGED — presented %s does not match the trusted key. "+
		"This may indicate a man-in-the-middle attack. Re-trust the host only if you changed it deliberately", e.Fingerprint)
}

// buildSSHClient connects a Docker client over SSH. The host's Address is
// "user@host[:port]". Authentication uses the server's own SSH agent and/or
// default private keys — no key material is stored in the app. The Docker API
// then talks to the remote unix socket tunnelled through the SSH connection.
func buildSSHClient(h *store.Host) (*client.Client, error) {
	user, addr, err := parseSSHAddress(h.Address)
	if err != nil {
		return nil, err
	}

	auth := sshAuthMethods()
	if len(auth) == 0 {
		return nil, fmt.Errorf("ssh: no auth available (start an ssh-agent or add a key under ~/.ssh)")
	}

	// Capture the host-key verification error explicitly: ssh.Dial wraps the
	// callback error in an opaque "handshake failed" string, so we stash the
	// typed error in a closure and return it verbatim below.
	var hostKeyErr error
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if err := verifyHostKey(h, key); err != nil {
				hostKeyErr = err
				return err
			}
			return nil
		},
		Timeout: 10 * time.Second,
	}

	sshConn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		if hostKeyErr != nil {
			return nil, hostKeyErr
		}
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	// Custom transport: every HTTP request the Docker client makes is dialled
	// to the remote daemon socket over the SSH connection. No Proxy is set, so a
	// HTTP(S)_PROXY in the environment can't hijack the tunnelled requests.
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return sshConn.Dial("unix", remoteDockerSocket)
			},
		},
	}

	// Option order is load-bearing: client.WithHost runs sockets.ConfigureTransport,
	// which OVERWRITES the transport's DialContext with a default TCP dialer. So
	// WithHTTPClient must come LAST, ensuring our SSH-tunnelling transport (with
	// its DialContext) is the one that survives — otherwise the client tries to
	// resolve the "docker.ssh" placeholder over DNS and fails.
	return client.NewClientWithOpts(
		client.WithHost("tcp://docker.ssh:2375"), // placeholder; our DialContext ignores the address
		client.WithAPIVersionNegotiation(),
		client.WithHTTPClient(httpClient),
	)
}

// parseSSHAddress splits "user@host[:port]" into (user, "host:port").
func parseSSHAddress(s string) (user, hostPort string, err error) {
	s = strings.TrimPrefix(s, "ssh://")
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return "", "", fmt.Errorf("ssh address must be user@host[:port]")
	}
	user = s[:at]
	host := s[at+1:]
	if user == "" || host == "" {
		return "", "", fmt.Errorf("ssh address must be user@host[:port]")
	}
	if !strings.Contains(host, ":") {
		host += ":22"
	}
	return user, host, nil
}

// verifyHostKey enforces our trust policy for the daemon's SSH host key:
//
//  1. If a key is pinned for this host (DB), the presented key must match it
//     exactly; otherwise it is a HostKeyMismatchError (possible MITM).
//  2. Otherwise fall back to ~/.ssh/known_hosts: a match is trusted, a recorded
//     host with a different key is a mismatch.
//  3. Otherwise the host is unknown — HostKeyUnknownError, so the operator can
//     review the fingerprint and explicitly trust it.
//
// This replaces the previous InsecureIgnoreHostKey, which trusted any key.
func verifyHostKey(h *store.Host, key ssh.PublicKey) error {
	if h.HostKey != "" {
		pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.HostKey))
		if err == nil && keysEqual(pinned, key) {
			return nil
		}
		return &HostKeyMismatchError{Fingerprint: ssh.FingerprintSHA256(key)}
	}

	if cb := knownHostsCallback(); cb != nil {
		err := cb("", &net.IPAddr{}, key) // hostname matching is irrelevant here; we compare keys
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) > 0 {
			// The host is recorded under some name with a different key.
			return &HostKeyMismatchError{Fingerprint: ssh.FingerprintSHA256(key)}
		}
		// ke.Want empty → simply not recorded; fall through to "unknown".
	}

	return &HostKeyUnknownError{Fingerprint: ssh.FingerprintSHA256(key), KeyType: key.Type()}
}

// knownHostsCallback returns a callback backed by ~/.ssh/known_hosts, or nil if
// the file is absent/unreadable (in which case only DB-pinned keys are trusted).
func knownHostsCallback() ssh.HostKeyCallback {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		log.Printf("ssh: cannot read %s: %v", path, err)
		return nil
	}
	return cb
}

// keysEqual reports whether two SSH public keys are byte-for-byte identical.
func keysEqual(a, b ssh.PublicKey) bool {
	if a == nil || b == nil {
		return false
	}
	am, bm := a.Marshal(), b.Marshal()
	if len(am) != len(bm) {
		return false
	}
	for i := range am {
		if am[i] != bm[i] {
			return false
		}
	}
	return true
}

// probeSSHHostKey connects far enough to capture the daemon's presented host
// key, accepting whatever is offered. It backs the explicit "trust" action:
// the caller pins the returned key after the operator approves the fingerprint.
// The host key is exchanged before authentication, so this succeeds (returns
// the key) even when SSH auth would later fail.
func probeSSHHostKey(h *store.Host) (keyLine, fingerprint string, err error) {
	user, addr, err := parseSSHAddress(h.Address)
	if err != nil {
		return "", "", err
	}

	var captured ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: sshAuthMethods(),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return nil
		},
		Timeout: 10 * time.Second,
	}

	conn, dialErr := ssh.Dial("tcp", addr, cfg)
	if conn != nil {
		_ = conn.Close()
	}
	if captured == nil {
		// Never got to key exchange — a real connectivity/timeout failure.
		if dialErr != nil {
			return "", "", fmt.Errorf("ssh dial %s: %w", addr, dialErr)
		}
		return "", "", fmt.Errorf("ssh: no host key presented by %s", addr)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(captured)))
	return line, ssh.FingerprintSHA256(captured), nil
}

// sshAuthMethods gathers auth from the ssh-agent and default key files.
func sshAuthMethods() []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	home, err := os.UserHomeDir()
	if err == nil {
		var signers []ssh.Signer
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			data, err := os.ReadFile(filepath.Join(home, ".ssh", name))
			if err != nil {
				continue
			}
			if signer, err := ssh.ParsePrivateKey(data); err == nil {
				signers = append(signers, signer)
			} else {
				log.Printf("ssh: skipping %s (passphrase-protected keys are not supported): %v", name, err)
			}
		}
		if len(signers) > 0 {
			methods = append(methods, ssh.PublicKeys(signers...))
		}
	}
	return methods
}
