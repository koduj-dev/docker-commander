package docker

import (
	"context"
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

	"github.com/koduj-dev/docker-commander/internal/store"
)

// remoteDockerSocket is where the daemon listens on the remote host.
const remoteDockerSocket = "/var/run/docker.sock"

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

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // local admin tool to your own hosts
		Timeout:         10 * time.Second,
	}

	sshConn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	// Custom transport: every HTTP request the Docker client makes is dialled
	// to the remote daemon socket over the SSH connection.
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return sshConn.Dial("unix", remoteDockerSocket)
			},
		},
	}

	return client.NewClientWithOpts(
		client.WithHTTPClient(httpClient),
		client.WithHost("http://docker.ssh"), // placeholder; the transport ignores it
		client.WithAPIVersionNegotiation(),
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
