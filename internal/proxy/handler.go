package proxy

import (
	"log"
	"net"
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
	newReverseProxy(b.addr).ServeHTTP(w, r)
	return true
}

// newReverseProxy builds the ReverseProxy for one resolved backend address.
//
// Uses Rewrite (not the deprecated NewSingleHostReverseProxy/Director shape)
// so a public client can never spoof X-Forwarded-For/-Host/-Proto: those
// headers are stripped from the outbound request before Rewrite runs
// (documented behavior of Rewrite, unlike Director, which preserves them),
// and SetXForwarded below sets them from the REAL inbound connection instead
// of trusting whatever the client sent. A backend that trusts these headers
// (a common pattern for a service that expects to sit behind a reverse
// proxy) would otherwise let any public client lie about its own IP, host,
// or scheme — exactly the trust boundary this proxy exists to enforce.
//
// X-Real-IP gets the same treatment, by hand: it's the one other
// single-client-IP header still in common use (nginx's own original
// convention, and what a stock chi middleware.RealIP prefers over
// X-Forwarded-For) but, unlike the X-Forwarded-* family, Go's Rewrite path
// does NOT strip it automatically — a client-sent value would otherwise
// reach the backend completely unexamined.
func newReverseProxy(backendAddr string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: backendAddr})
			pr.Out.Host = pr.In.Host // preserve the mapped domain, not 127.0.0.1:<port>
			pr.SetXForwarded()
			if clientIP, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
				pr.Out.Header.Set("X-Real-IP", clientIP)
			} else {
				pr.Out.Header.Del("X-Real-IP")
			}
		},
	}
}
