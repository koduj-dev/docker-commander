package api

import (
	"net"
	"net/http"
	"strings"
)

// clientIP normalises r.RemoteAddr to the real client IP, trusting the
// X-Forwarded-For chain ONLY when the immediate connection comes from a
// configured trusted proxy. With no trusted proxies (the default) forwarded
// headers are ignored entirely and the TCP peer is used as-is.
//
// This replaces chi's middleware.RealIP, which unconditionally honours
// X-Forwarded-For / X-Real-IP and is therefore spoofable — chi itself now
// deprecates it for that reason. Every IP-based decision downstream (login and
// OAuth rate limits, the loopback 2FA exemption, audit) reads r.RemoteAddr, so
// trusting forwarded headers from untrusted clients let a remote attacker forge
// any address (claim loopback, or rotate IPs to evade brute-force throttling).
func (s *Server) clientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := host
		peer := net.ParseIP(host)
		// Only consult forwarded headers when the peer is a trusted proxy.
		if peer != nil && ipInAny(peer, s.cfg.TrustedProxies) {
			if real := realClientFromXFF(r, s.cfg.TrustedProxies); real != "" {
				ip = real
			}
		}
		// Normalise to the bare client IP (no ephemeral port). Downstream keys
		// rate limits and audit on r.RemoteAddr as a string, so a stable per-IP
		// value is what we want — this also matches RealIP's old behaviour and
		// fixes the previously port-keyed (i.e. ineffective) limiting on direct
		// connections.
		r.RemoteAddr = ip
		next.ServeHTTP(w, r)
	})
}

// realClientFromXFF resolves the client address from the X-Forwarded-For chain
// using the rightmost-untrusted-hop rule: walk the appended list from the right
// (closest hop first) and return the first address that is NOT a trusted proxy —
// that is the furthest point we can still vouch for. A client can prepend
// arbitrary entries, but it cannot push an untrusted hop to the right of the
// real one, so spoofing only changes values we already discard. Returns "" when
// the header is absent (caller keeps the peer).
func realClientFromXFF(r *http.Request, trusted []*net.IPNet) string {
	var ips []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(h, ",") {
			if p := strings.TrimSpace(part); p != "" {
				ips = append(ips, p)
			}
		}
	}
	if len(ips) == 0 {
		return ""
	}
	for i := len(ips) - 1; i >= 0; i-- {
		ip := net.ParseIP(ips[i])
		if ip == nil {
			continue
		}
		if !ipInAny(ip, trusted) {
			return ips[i] // first untrusted hop from the right = the real client
		}
	}
	// Every hop is itself a trusted proxy → the original client is the leftmost.
	return ips[0]
}

func ipInAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
