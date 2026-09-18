package proxy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// backend is a resolved dial target for one domain_mappings entry.
type backend struct {
	addr string // "127.0.0.1:<PublicPort>" — never derived from stored config directly, see resolveBackend
}

// resolveBackend resolves m to a live, dialable local backend.
//
// Deliberately does NOT trust m.TargetPort as a dial target — it is only
// ever a matching key against a container's live PrivatePort. The returned
// address always comes from Docker's own PublicPort for a container that is
// actually running, actually belongs to m's project (by compose project
// label matching the project's own Slug), and actually has the compose
// service name m.Service. Treating TargetPort as a host-local port to dial
// directly would let whoever holds "projects" write access on ANY project —
// including this one — point the proxy at an arbitrary host-local port (say,
// DC's own admin port, or an unrelated service on the loopback interface)
// merely by editing a mapping, with no real service behind it required.
//
// Returns a wrapped error (never panics, never returns a zero-value backend
// with a nil error) when: the project no longer exists, the project is not
// local, no running container in that compose project has the mapped
// service name, or that container doesn't actually publish m.TargetPort as
// a TCP port. The caller (handler.go) turns any error into a 502.
func (p *Proxy) resolveBackend(ctx context.Context, m store.DomainMapping) (backend, error) {
	proj, err := p.store.ProjectByID(ctx, m.ProjectID)
	if err != nil {
		return backend{}, fmt.Errorf("proxy: project %d: %w", m.ProjectID, err)
	}
	// Re-verified here even though HostPolicy already checked it at cert
	// issuance time — issuance and this resolution happen at different
	// moments (a cert can outlive the mapping/project state it was issued
	// under), so this function must stand on its own as a security boundary,
	// not lean on having already been screened once upstream.
	if proj.HostID != 0 {
		return backend{}, fmt.Errorf("proxy: project %d is not local (hostID=%d)", proj.ID, proj.HostID)
	}
	stacks, err := p.docker.ListStacks(ctx, 0) // hostID<=0 resolves the local daemon
	if err != nil {
		return backend{}, fmt.Errorf("proxy: listing local stacks: %w", err)
	}
	for _, st := range stacks {
		if st.Project != proj.Slug {
			continue
		}
		for _, c := range st.Containers {
			if c.Service != m.Service || c.State != "running" {
				continue
			}
			for _, port := range c.Ports {
				if port.Type == "tcp" && int(port.PrivatePort) == m.TargetPort && port.PublicPort != 0 {
					return backend{addr: fmt.Sprintf("127.0.0.1:%d", port.PublicPort)}, nil
				}
			}
		}
	}
	return backend{}, fmt.Errorf("proxy: no running container for service %q publishing TCP port %d in project %q", m.Service, m.TargetPort, proj.Slug)
}

// backendCache holds the most recently resolved backend per domain for a
// short TTL, so a burst of requests to one domain doesn't hit the Docker API
// on every single one. Not a background poller: resolution still only
// happens lazily, on the first request after the TTL expires — a redeploy
// is picked up within one TTL window of the next request. Both a successful
// resolution AND an error are cached for the same TTL, so a currently-down
// backend doesn't get re-resolved (and re-hit ListStacks) on every request
// either.
type backendCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]cachedBackend
}

type cachedBackend struct {
	b       backend
	err     error
	expires time.Time
}

func newBackendCache(ttl time.Duration) *backendCache {
	return &backendCache{ttl: ttl, m: make(map[string]cachedBackend)}
}

// resolveBackendCached is resolveBackend, fronted by the TTL cache above.
func (p *Proxy) resolveBackendCached(ctx context.Context, m store.DomainMapping) (backend, error) {
	now := time.Now()
	p.cache.mu.Lock()
	if c, ok := p.cache.m[m.Domain]; ok && now.Before(c.expires) {
		p.cache.mu.Unlock()
		return c.b, c.err
	}
	p.cache.mu.Unlock()

	b, err := p.resolveBackend(ctx, m)

	p.cache.mu.Lock()
	p.cache.m[m.Domain] = cachedBackend{b: b, err: err, expires: now.Add(p.cache.ttl)}
	p.cache.mu.Unlock()
	return b, err
}
