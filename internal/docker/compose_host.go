package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// ComposeHostEnv returns the environment additions that make the `docker compose`
// CLI target the given host's daemon, plus a cleanup func to call when the
// command finishes. For the local host it returns (nil, noop). For a TCP host
// with TLS it materialises the stored certs into a private temp dir and points
// DOCKER_CERT_PATH at it (the dir is removed by cleanup). For SSH it sets a
// DOCKER_HOST=ssh:// URL (the CLI tunnels via the system ssh client).
func ComposeHostEnv(h *store.Host) (env []string, cleanup func(), err error) {
	noop := func() {}
	if h == nil || h.Kind == "" || h.Kind == "local" {
		return nil, noop, nil
	}
	switch h.Kind {
	case "tcp":
		if h.Address == "" {
			return nil, noop, fmt.Errorf("host %q has no address", h.Name)
		}
		env = []string{"DOCKER_HOST=" + h.Address}
		if h.TLSCA == "" && h.TLSCert == "" && h.TLSKey == "" {
			return env, noop, nil // plain (insecure) TCP
		}
		dir, err := writeTLSCerts(h)
		if err != nil {
			return nil, noop, err
		}
		env = append(env, "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH="+dir)
		return env, func() { _ = os.RemoveAll(dir) }, nil
	case "ssh":
		if h.Address == "" {
			return nil, noop, fmt.Errorf("host %q has no address", h.Name)
		}
		addr := h.Address
		if !strings.HasPrefix(addr, "ssh://") {
			addr = "ssh://" + addr
		}
		return []string{"DOCKER_HOST=" + addr}, noop, nil
	default:
		return nil, noop, fmt.Errorf("unsupported host kind %q", h.Kind)
	}
}

// writeTLSCerts materialises a TCP host's PEM material into a 0700 temp dir as
// ca.pem / cert.pem / key.pem (0600) for the docker CLI's DOCKER_CERT_PATH. The
// caller removes the dir once the command returns, so the key isn't left on disk.
func writeTLSCerts(h *store.Host) (string, error) {
	dir, err := os.MkdirTemp("", "dc-compose-tls-*")
	if err != nil {
		return "", err
	}
	files := []struct {
		name, pem string
		mode      os.FileMode
	}{
		{"ca.pem", h.TLSCA, 0o600},
		{"cert.pem", h.TLSCert, 0o600},
		{"key.pem", h.TLSKey, 0o600},
	}
	for _, f := range files {
		if f.pem == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.pem), f.mode); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// ComposeBindMounts returns the host-path bind mounts declared in a resolved
// compose config (the `docker compose config --format json` output), as
// "service: source → target" strings. Used to block a remote deploy that would
// bind-mount paths the remote daemon can't see.
func ComposeBindMounts(configJSON []byte) ([]string, error) {
	var cfg struct {
		Services map[string]struct {
			Volumes []struct {
				Type   string `json:"type"`
				Source string `json:"source"`
				Target string `json:"target"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	var binds []string
	for name, svc := range cfg.Services {
		for _, v := range svc.Volumes {
			if v.Type == "bind" {
				binds = append(binds, fmt.Sprintf("%s: %s → %s", name, v.Source, v.Target))
			}
		}
	}
	sort.Strings(binds)
	return binds, nil
}
