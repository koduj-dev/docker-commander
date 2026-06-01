// Package docker manages connections to one or more Docker engines and exposes
// a domain-shaped API (containers, networks, stats, logs) for the rest of the
// app. Connections are created lazily per host and cached.
package docker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/docker/docker/client"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Manager owns Docker client connections keyed by host ID.
type Manager struct {
	store *store.Store

	mu      sync.Mutex
	clients map[int64]*client.Client
}

// NewManager returns a manager that resolves hosts from the store.
func NewManager(s *store.Store) *Manager {
	return &Manager{store: s, clients: make(map[int64]*client.Client)}
}

// Client returns a connected Docker client for the given host ID, creating and
// caching it on first use. A hostID <= 0 means "the default local host", which
// lets clients (REST and WebSocket) omit the host when targeting localhost.
func (m *Manager) Client(ctx context.Context, hostID int64) (*client.Client, error) {
	if hostID <= 0 {
		id, err := m.defaultHostID(ctx)
		if err != nil {
			return nil, err
		}
		hostID = id
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[hostID]; ok {
		return c, nil
	}
	h, err := m.store.HostByID(ctx, hostID)
	if err != nil {
		return nil, err
	}
	c, err := buildClient(h)
	if err != nil {
		return nil, err
	}
	m.clients[hostID] = c
	return c, nil
}

// defaultHostID returns the local host's ID, falling back to the first host.
func (m *Manager) defaultHostID(ctx context.Context) (int64, error) {
	hosts, err := m.store.ListHosts(ctx)
	if err != nil {
		return 0, err
	}
	for _, h := range hosts {
		if h.Kind == "local" {
			return h.ID, nil
		}
	}
	if len(hosts) > 0 {
		return hosts[0].ID, nil
	}
	return 0, store.ErrNotFound
}

// Close disconnects all cached clients.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		_ = c.Close()
	}
	m.clients = make(map[int64]*client.Client)
}

// buildClient constructs a Docker client appropriate for the host kind.
func buildClient(h *store.Host) (*client.Client, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}

	switch h.Kind {
	case "local", "":
		// FromEnv honours DOCKER_HOST/DOCKER_CERT_PATH and otherwise falls
		// back to the OS default socket (unix socket / windows named pipe).
		opts = append(opts, client.FromEnv)
		if h.Address != "" {
			opts = append(opts, client.WithHost(h.Address))
		}

	case "tcp":
		if h.Address == "" {
			return nil, errors.New("tcp host requires an address")
		}
		opts = append(opts, client.WithHost(h.Address))
		if h.TLSCA != "" || h.TLSCert != "" {
			httpClient, err := tlsHTTPClient(h)
			if err != nil {
				return nil, err
			}
			opts = append(opts, client.WithHTTPClient(httpClient))
		}

	case "ssh":
		// SSH transport requires a helper connection; tracked for a later
		// iteration. The host model already persists everything needed.
		return nil, errors.New("ssh hosts are not yet supported")

	default:
		return nil, fmt.Errorf("unknown host kind %q", h.Kind)
	}

	return client.NewClientWithOpts(opts...)
}

// tlsHTTPClient builds an *http.Client trusting the host CA and presenting the
// client certificate, all from PEM material stored in the DB (no temp files).
func tlsHTTPClient(h *store.Host) (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if h.TLSCA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(h.TLSCA)) {
			return nil, errors.New("invalid TLS CA PEM")
		}
		cfg.RootCAs = pool
	}
	if h.TLSCert != "" && h.TLSKey != "" {
		cert, err := tls.X509KeyPair([]byte(h.TLSCert), []byte(h.TLSKey))
		if err != nil {
			return nil, fmt.Errorf("invalid TLS client keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}, nil
}
