package docker

import (
	"os"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func envHas(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func TestComposeBindMounts(t *testing.T) {
	cfg := []byte(`{
		"services": {
			"web": {"volumes": [
				{"type": "bind", "source": "/srv/app/config", "target": "/etc/app"},
				{"type": "volume", "source": "data", "target": "/var/lib"}
			]},
			"db": {"volumes": [
				{"type": "tmpfs", "target": "/tmp"},
				{"type": "bind", "source": "./seed", "target": "/seed"}
			]}
		}
	}`)
	binds, err := ComposeBindMounts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) != 2 {
		t.Fatalf("expected 2 bind mounts, got %v", binds)
	}
	// Named volume and tmpfs must NOT be flagged.
	joined := strings.Join(binds, " | ")
	if strings.Contains(joined, "data") || strings.Contains(joined, "tmpfs") {
		t.Errorf("non-bind mount leaked into results: %v", binds)
	}
	if !strings.Contains(joined, "/srv/app/config") || !strings.Contains(joined, "./seed") {
		t.Errorf("bind sources missing: %v", binds)
	}
}

func TestComposeBindMounts_NamedVolumesOnly(t *testing.T) {
	cfg := []byte(`{"services": {"web": {"volumes": [{"type": "volume", "source": "data", "target": "/var"}]}}}`)
	binds, err := ComposeBindMounts(cfg)
	if err != nil || len(binds) != 0 {
		t.Errorf("named-volume-only project should have no bind mounts: %v (err %v)", binds, err)
	}
}

func TestComposeHostEnv_Local(t *testing.T) {
	for _, h := range []*store.Host{nil, {Kind: "local"}, {Kind: ""}} {
		env, cleanup, err := ComposeHostEnv(h)
		if err != nil || env != nil {
			t.Errorf("local host should yield nil env, got %v (err %v)", env, err)
		}
		cleanup() // must be safe
	}
}

func TestComposeHostEnv_TCPPlain(t *testing.T) {
	env, cleanup, err := ComposeHostEnv(&store.Host{Kind: "tcp", Name: "edge", Address: "tcp://10.0.0.9:2375"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !envHas(env, "DOCKER_HOST=tcp://10.0.0.9:2375") {
		t.Errorf("missing DOCKER_HOST, got %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "DOCKER_CERT_PATH") {
			t.Errorf("plain TCP must not set DOCKER_CERT_PATH: %v", env)
		}
	}
}

func TestComposeHostEnv_SSH(t *testing.T) {
	env, _, err := ComposeHostEnv(&store.Host{Kind: "ssh", Address: "deploy@host.lan"})
	if err != nil || !envHas(env, "DOCKER_HOST=ssh://deploy@host.lan") {
		t.Errorf("ssh env = %v (err %v)", env, err)
	}
	// An address already carrying the scheme must not be double-prefixed.
	env2, _, _ := ComposeHostEnv(&store.Host{Kind: "ssh", Address: "ssh://deploy@host.lan"})
	if !envHas(env2, "DOCKER_HOST=ssh://deploy@host.lan") {
		t.Errorf("ssh:// prefix double-applied: %v", env2)
	}
}

// PENTEST: a TCP+TLS host materialises certs to a private dir (0700) with the
// key 0600, and cleanup wipes them — the private key is never left on disk.
func TestComposeHostEnv_TCPTLS_CertsLockedAndCleaned(t *testing.T) {
	h := &store.Host{
		Kind: "tcp", Name: "secure", Address: "tcp://reg.lan:2376",
		TLSCA: "CA-PEM", TLSCert: "CERT-PEM", TLSKey: "KEY-PEM",
	}
	env, cleanup, err := ComposeHostEnv(h)
	if err != nil {
		t.Fatal(err)
	}
	if !envHas(env, "DOCKER_TLS_VERIFY=1") {
		t.Errorf("TLS host should set DOCKER_TLS_VERIFY, got %v", env)
	}
	var dir string
	for _, e := range env {
		if strings.HasPrefix(e, "DOCKER_CERT_PATH=") {
			dir = strings.TrimPrefix(e, "DOCKER_CERT_PATH=")
		}
	}
	if dir == "" {
		t.Fatal("DOCKER_CERT_PATH not set")
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("cert dir mode = %v (err %v), want 0700", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(dir + "/key.pem"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key.pem mode = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cert dir should be removed after cleanup, stat err = %v", err)
	}
}

func TestComposeHostEnv_TCPNoAddress(t *testing.T) {
	if _, _, err := ComposeHostEnv(&store.Host{Kind: "tcp", Name: "x"}); err == nil {
		t.Error("a TCP host with no address should error")
	}
}
