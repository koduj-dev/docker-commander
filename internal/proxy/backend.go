package proxy

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// backend is a resolved dial target for one domain_mappings entry.
type backend struct {
	addr string // host:port — never derived from stored config directly, see resolveBackend
}

// bindDialAddr turns a Docker-reported (ip, port) publish binding into a
// dial address FROM THIS HOST. Docker publishes to a specific IP, not
// always the wildcard — a container can legitimately be published on
// 192.0.2.10:8443 while something else entirely owns 127.0.0.1:8443, and
// those two bindings coexist without conflict. Assuming loopback regardless
// of what Docker actually reported would dial whatever unrelated thing
// happens to be listening there instead of the container this mapping is
// actually about. "0.0.0.0"/"" and "::" are Docker's own wildcard-bind
// spellings — reachable via loopback, so they're the one case translated
// rather than used verbatim; every other reported IP is dialed exactly as
// given, since it's Docker's own live data, not a guess.
func bindDialAddr(ip string, port uint16) string {
	switch ip {
	case "", "0.0.0.0":
		ip = "127.0.0.1"
	case "::":
		ip = "::1"
	}
	return net.JoinHostPort(ip, fmt.Sprintf("%d", port))
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
	// A Host row of Kind "local" doesn't guarantee "the same machine (or
	// network namespace) this process runs in" — it honors DOCKER_HOST
	// (internal/docker's buildClient uses client.FromEnv), which an
	// operator can point at a remote tcp:// daemon while still leaving
	// HostID at 0. dialing 127.0.0.1/a bind IP reported by THAT daemon
	// would then target this process's own loopback, not the daemon's —
	// refuse rather than guess. A unix/npipe socket is the one case this
	// phase can trust (same daemon host as far as the API is concerned);
	// see NEXT.md for the still-open containerized-Docker-Commander case,
	// where the socket is local but the network namespace may not be.
	cli, err := p.docker.Client(ctx, 0)
	if err != nil {
		return backend{}, fmt.Errorf("proxy: local docker client: %w", err)
	}
	if host := cli.DaemonHost(); !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return backend{}, fmt.Errorf("proxy: local daemon is not a local socket (%s) — refusing to guess a dial address", host)
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
					return backend{addr: bindDialAddr(port.IP, port.PublicPort)}, nil
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
//
// Concurrent requests for the same domain arriving while the cache is cold
// share ONE resolveBackend call via group (singleflight), rather than each
// issuing their own ListStacks — a burst against a public mapped domain
// (ordinary traffic, or deliberately) would otherwise fan out into one
// Docker API call PER concurrent request every time the TTL lapses, which
// defeats the point of caching at all and can overload the daemon.
type backendCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	m     map[string]cachedBackend
	group singleflight.Group
}

type cachedBackend struct {
	b       backend
	err     error
	expires time.Time
}

func newBackendCache(ttl time.Duration) *backendCache {
	return &backendCache{ttl: ttl, m: make(map[string]cachedBackend)}
}

// backendResult bundles resolveBackend's two return values so they travel
// together through singleflight.Group.Do, whose own signature only carries
// one (any, error) pair — the group's own error slot is deliberately left
// unused (always nil) so a resolution failure is still shared identically
// with every waiter via this struct, not treated as a singleflight-level
// failure with different semantics.
type backendResult struct {
	b   backend
	err error
}

// resolveTimeout bounds a coalesced resolution (see resolveBackendCached) —
// it must never run forever just because it's no longer tied to any single
// waiter's request lifetime.
const resolveTimeout = 10 * time.Second

// resolveBackendCached is resolveBackend, fronted by the TTL cache above.
//
// The coalesced flight (see backendCache's own doc comment for why it's
// coalesced at all) runs under an INDEPENDENT, bounded context — never any
// one waiter's r.Context(). Using a waiter's context would mean the first
// caller to arrive disconnecting (net/http cancels its context on
// disconnect) cancels the lookup for every OTHER request sharing that
// flight too, and would store that cancellation error as the domain's
// shared cache entry — turning one impatient/disconnecting client into a
// TTL-long outage for every subsequent request to that domain. Each waiter
// still honors ITS OWN context via the select below: if ITS request is
// canceled, it stops waiting immediately without affecting the flight or
// any other waiter.
func (p *Proxy) resolveBackendCached(ctx context.Context, m store.DomainMapping) (backend, error) {
	p.cache.mu.Lock()
	if c, ok := p.cache.m[m.Domain]; ok && time.Now().Before(c.expires) {
		p.cache.mu.Unlock()
		return c.b, c.err
	}
	p.cache.mu.Unlock()

	ch := p.cache.group.DoChan(m.Domain, func() (any, error) {
		// Re-check: a caller delayed between the miss above and actually
		// entering this flight may find another flight already populated
		// the cache (its own singleflight key had already been released).
		p.cache.mu.Lock()
		if c, ok := p.cache.m[m.Domain]; ok && time.Now().Before(c.expires) {
			p.cache.mu.Unlock()
			return backendResult{b: c.b, err: c.err}, nil
		}
		p.cache.mu.Unlock()

		rctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()
		b, err := p.resolveFn(rctx, m)
		p.cache.mu.Lock()
		// expires is computed from NOW — after resolution completes — not
		// from when the miss was first observed. A ListStacks call taking
		// close to (or longer than) the TTL would otherwise store an
		// already-expired entry, forcing the very next request to redo the
		// same work the cache exists to avoid.
		p.cache.m[m.Domain] = cachedBackend{b: b, err: err, expires: time.Now().Add(p.cache.ttl)}
		p.cache.mu.Unlock()
		return backendResult{b: b, err: err}, nil
	})

	select {
	case res := <-ch:
		r := res.Val.(backendResult)
		return r.b, r.err
	case <-ctx.Done():
		return backend{}, ctx.Err()
	}
}
