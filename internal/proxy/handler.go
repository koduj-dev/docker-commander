package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// TryServeHTTP serves r if r.Host resolves to a live, locally-routable
// domain_mappings entry, and reports whether it did.
//
// Returns false WITHOUT writing anything when there is no such mapping —
// the caller (dispatch.CombinedHandler) decides the fallback, which must
// never be "serve it anyway." Returns true in every other case, including
// when the mapping exists but has no live backend right now (a container
// that isn't running, or doesn't publish the mapped port): that is still a
// definite answer — StatusBadGateway — not a fallthrough to some other
// handler, because from the caller's perspective "this domain is a known
// proxy target, just currently down" must never be confused with "this
// domain isn't a proxy target at all."
func (p *Proxy) TryServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	m, _, ok := p.eligibleMapping(ctx, hostOnly(r.Host))
	if !ok {
		return false
	}
	b, err := p.resolveBackendCached(ctx, m)
	if err != nil {
		log.Printf("proxy: %s: %v", m.Domain, err)
		http.Error(w, "backend unavailable", http.StatusBadGateway)
		return true
	}
	rp := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: b.addr})
	rp.ServeHTTP(w, r)
	return true
}
