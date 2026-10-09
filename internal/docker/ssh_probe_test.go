package docker

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// PENTEST: the host-key probe runs against a server nobody has approved yet,
// so it must take the key and stop. It used to go on to authenticate, offering
// the unverified server every public key in the agent and the default key
// files. A real SSH server here counts every authentication attempt.
func TestPentestSSHHostKeyProbeSendsNoCredentials(t *testing.T) {
	// A key the old probe would have offered: a default key file in $HOME, no agent.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			attempts.Add(1)
			return nil, errHostKeyCaptured // reject; only the attempt matters
		},
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			attempts.Add(1)
			return nil, errHostKeyCaptured
		},
	}
	cfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	served := make(chan struct{})
	go func() {
		defer close(served)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _, _, _ = ssh.NewServerConn(c, cfg) // fails once the client hangs up
	}()

	_, fp, err := probeSSHHostKey(&store.Host{Kind: "ssh", Address: "probe@" + l.Addr().String()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	<-served
	if want := ssh.FingerprintSHA256(signer.PublicKey()); fp != want {
		t.Errorf("fingerprint %s, want %s", fp, want)
	}
	if n := attempts.Load(); n != 0 {
		t.Errorf("SECURITY: the probe tried to authenticate %d time(s) to a server whose key isn't trusted yet", n)
	}
}
