package proxy

import (
	"crypto/tls"
	"net/http"
	"strings"
)

// hostOnly strips an optional ":port" suffix from a Host header/SNI value,
// so "example.com:443" and "example.com" compare equal. IPv6 literals
// ("[::1]:443") are left as-is by net.SplitHostPort's own handling; domains
// this package ever matches are always plain hostnames, never IP literals
// (phase 1's validFQDN already rejects those at write time).
func hostOnly(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}

// toSet builds a lookup set of configured admin domains, lowercased — DNS
// names are case-insensitive, and autocert.Manager itself canonicalizes a
// ClientHelloInfo.ServerName (via IDNA ToASCII, which also folds case)
// before ever calling a HostPolicy. Without matching that here, an admin
// domain configured with any uppercase letter (nothing stops an operator
// from typing DC_ACME_DOMAINS=Admin.Example.com) would never match a
// perfectly ordinary lowercase SNI/Host, misrouting real admin traffic to
// the proxy branch — which then correctly refuses it, but for the wrong
// reason: casing, not eligibility.
func toSet(domains []string) map[string]bool {
	set := make(map[string]bool, len(domains))
	for _, d := range domains {
		set[strings.ToLower(strings.TrimSpace(d))] = true
	}
	return set
}

// CombinedGetCertificate returns a tls.Config.GetCertificate that dispatches
// by SNI: an admin domain goes to adminMgr (Docker Commander's own,
// unchanged autocert manager); everything else falls to proxyMgr, whose own
// HostPolicy (see hostpolicy.go) is what actually decides whether an
// unrecognized name gets refused rather than silently attempted.
func CombinedGetCertificate(adminMgr, proxyMgr tlsCertGetter, adminDomains []string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	admin := toSet(adminDomains)
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if admin[strings.ToLower(hello.ServerName)] {
			return adminMgr.GetCertificate(hello)
		}
		return proxyMgr.GetCertificate(hello)
	}
}

// tlsCertGetter is the one method of *autocert.Manager this package needs —
// declared as an interface so dispatch_test.go can exercise the dispatch
// logic with fakes instead of constructing real autocert.Managers (which
// would try to touch a cache directory and, on first real use, the network).
type tlsCertGetter interface {
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// CombinedHandler dispatches an HTTP request: an admin domain goes to
// adminHandler (today's single chi mux, unchanged); a domain with a live
// local mapping goes to the proxy; anything else is rejected — see the
// default-deny note below, this is NOT "fall through to the admin app."
func CombinedHandler(adminHandler http.Handler, adminDomains []string, p *Proxy) http.Handler {
	admin := toSet(adminDomains)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(hostOnly(r.Host))

		// Defense in depth against SNI/Host decorrelation: GetCertificate
		// picks a certificate by TLS SNI (ClientHelloInfo.ServerName); this
		// handler routes by the HTTP Host header — two fields a client
		// controls independently of each other over the SAME TLS
		// connection. Without this check, completing a handshake with
		// SNI = an admin domain (a legitimately obtainable certificate —
		// nothing secret about it) and then sending Host = a mapped domain
		// would reach the proxy branch under a certificate that was never
		// actually validated for that mapped domain, because HostPolicy
		// only ever runs against the SNI used AT ISSUANCE time, never
		// against a later request's Host header. Reject outright rather
		// than let the two disagree.
		if r.TLS != nil && r.TLS.ServerName != "" && !strings.EqualFold(r.TLS.ServerName, host) {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}

		if admin[host] {
			adminHandler.ServeHTTP(w, r)
			return
		}
		if p.TryServeHTTP(w, r) {
			return
		}
		// Neither the admin domain nor a live local mapping. Critically,
		// this must NOT fall through to adminHandler: a domain_mappings row
		// (and the certificate issued for it) can outlive its own
		// eligibility — the row gets deleted, or its project's HostID
		// changes, while an already-issued ACME certificate stays valid and
		// cached for up to its full lifetime. That means this branch is
		// reached in normal operation, not just under attack, and serving
		// the admin login UI here would expose it on a public domain nobody
		// configured it for.
		http.NotFound(w, r)
	})
}
