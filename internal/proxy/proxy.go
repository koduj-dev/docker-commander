// Package proxy is the embedded per-container reverse proxy: phase 2 of
// NEXT.md's "Per-container domain + TLS". Phase 1 (internal/store's
// domain_mappings + internal/api's domain_handlers) recorded intent only —
// "route this public domain to this project's service+port" — with nothing
// listening. This package makes that real, for local-host projects only
// (see docs/projects.md and NEXT.md for the still-open remote-host phase).
//
// The design deliberately reuses two things already in the codebase rather
// than adding a dependency: golang.org/x/crypto/acme/autocert (already used
// by internal/acme for Docker Commander's own admin-domain TLS) and stdlib
// net/http/httputil.ReverseProxy. A second autocert.Manager lives here,
// separate from the admin one, with its own cert cache directory and a
// DYNAMIC HostPolicy (internal/acme's is a fixed autocert.HostWhitelist,
// which can't express "whatever domain_mappings currently contains").
//
// cmd/dockercmd/main.go combines this package's Handler/HostPolicy with the
// admin server's own chi mux and autocert manager onto ONE shared listener,
// dispatched by SNI (for certificates) and Host header (for requests) — see
// dispatch.go for exactly how, and why naive SNI/Host trust is dangerous.
package proxy

import (
	"context"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Config configures the proxy's own ACME manager. Mirrors internal/acme's
// NewManager parameters, but the resulting Manager's HostPolicy is dynamic
// (see hostpolicy.go), not a fixed whitelist.
type Config struct {
	ACMEEmail    string
	CacheDir     string
	DirectoryURL string
}

// Proxy holds everything the embedded reverse proxy needs: the store (to
// resolve domain_mappings and their owning project), the Docker manager (to
// find where a project's service is actually listening), its own autocert
// manager, and a short-lived cache of resolved backends.
type Proxy struct {
	store  *store.Store
	docker *docker.Manager
	mgr    *autocert.Manager
	cache  *backendCache
	// resolveFn is resolveBackend by default (see New) — a field rather than
	// a direct call so backend_test.go can substitute a counting/blocking
	// stub to test resolveBackendCached's coalescing without a real Docker
	// daemon.
	resolveFn func(context.Context, store.DomainMapping) (backend, error)
}

// New builds a Proxy and its own autocert.Manager (HostPolicy wired to this
// Proxy's own dynamic check — see hostpolicy.go). Does not start anything;
// the manager's GetCertificate is only ever called on demand, during a TLS
// handshake, by the caller's tls.Config (see dispatch.go).
func New(st *store.Store, dm *docker.Manager, cfg Config) *Proxy {
	p := &Proxy{
		store:  st,
		docker: dm,
		cache:  newBackendCache(5 * time.Second),
	}
	p.resolveFn = p.resolveBackend
	p.mgr = &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cfg.CacheDir),
		HostPolicy: p.HostPolicy,
		Email:      cfg.ACMEEmail,
	}
	if cfg.DirectoryURL != "" {
		p.mgr.Client = &acme.Client{DirectoryURL: cfg.DirectoryURL}
	}
	return p
}

// ACMEManager returns the proxy's own autocert.Manager, for
// dispatch.CombinedGetCertificate to delegate to.
func (p *Proxy) ACMEManager() *autocert.Manager { return p.mgr }

// tlsModeACME is the only TLS mode this phase serves. The store also allows
// the reserved value "none" (plain HTTP, not implemented), and a row can
// carry it without going through the API's validation — a recovery bundle is
// imported straight into the store — so eligibility has to refuse it here
// rather than assume every stored row was API-validated.
const tlsModeACME = "acme"

// eligibleMapping resolves domain to a DomainMapping that is eligible for
// this phase: served with ACME TLS, and owned by an existing local project
// (HostID == 0). Returns (mapping, project, true) only when every check
// passes; every caller in this package (HostPolicy, the backend resolver, the
// HTTP handler) goes through this single function so the eligibility rule
// can't drift between them.
func (p *Proxy) eligibleMapping(ctx context.Context, domain string) (store.DomainMapping, store.Project, bool) {
	m, err := p.store.DomainMappingByDomain(ctx, domain)
	if err != nil {
		return store.DomainMapping{}, store.Project{}, false
	}
	if m.TLSMode != tlsModeACME {
		return store.DomainMapping{}, store.Project{}, false
	}
	proj, err := p.store.ProjectByID(ctx, m.ProjectID)
	if err != nil {
		return store.DomainMapping{}, store.Project{}, false
	}
	if proj.HostID != 0 {
		return store.DomainMapping{}, store.Project{}, false
	}
	return *m, *proj, true
}
