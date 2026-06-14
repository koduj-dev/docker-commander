package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
)

func cidrs(t *testing.T, specs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, s := range specs {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("bad cidr %q: %v", s, err)
		}
		out = append(out, n)
	}
	return out
}

// runClientIP sends a request with the given peer + headers through the clientIP
// middleware and returns the resulting RemoteAddr and whether isLoopback(r) sees
// it as loopback (the input to the 2FA exemption / pprof-style decisions).
func runClientIP(trusted []*net.IPNet, remoteAddr string, xff []string) (gotAddr string, loopback bool) {
	s := &Server{cfg: config.Config{TrustedProxies: trusted}}
	h := s.clientIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAddr = r.RemoteAddr
		loopback = isLoopback(r)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remoteAddr
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return gotAddr, loopback
}

// TestClientIP_SpoofRejectedWithoutTrustedProxy is the core regression guard:
// with no trusted proxies, a remote client's X-Forwarded-For is ignored, so it
// cannot forge a loopback address to steal the 2FA exemption.
func TestClientIP_SpoofRejectedWithoutTrustedProxy(t *testing.T) {
	// Attacker connects from 10.0.0.5 and claims to be loopback.
	addr, loop := runClientIP(nil, "10.0.0.5:4444", []string{"127.0.0.1"})
	if loop {
		t.Error("spoofed X-Forwarded-For: 127.0.0.1 from an untrusted peer must NOT be treated as loopback")
	}
	if addr != "10.0.0.5" {
		t.Errorf("RemoteAddr should be the real peer IP 10.0.0.5, got %q", addr)
	}

	// Even claiming ::1 / a private range must be ignored.
	if _, loop := runClientIP(nil, "8.8.8.8:1", []string{"::1"}); loop {
		t.Error("spoofed ::1 must not be treated as loopback")
	}
}

// TestClientIP_TrustedProxyResolvesRealClient: behind a declared proxy, the real
// client IP comes from X-Forwarded-For — a remote client is seen as remote (no
// exemption), a genuinely-local one as loopback.
func TestClientIP_TrustedProxyResolvesRealClient(t *testing.T) {
	trusted := cidrs(t, "127.0.0.0/8", "::1/128")

	// Remote user via the local proxy → real client 203.0.113.9 → not loopback.
	addr, loop := runClientIP(trusted, "127.0.0.1:5555", []string{"203.0.113.9"})
	if loop {
		t.Error("remote client behind a trusted proxy must not be loopback")
	}
	if addr != "203.0.113.9" {
		t.Errorf("expected real client 203.0.113.9, got %q", addr)
	}

	// Genuinely local user hitting the local proxy → XFF carries 127.0.0.1.
	if _, loop := runClientIP(trusted, "127.0.0.1:5555", []string{"127.0.0.1"}); !loop {
		t.Error("a genuinely local client via a trusted proxy should be loopback")
	}
}

// TestClientIP_RightmostUntrusted: a client can prepend junk to X-Forwarded-For,
// but the proxy appends the true peer on the right, so the rightmost-untrusted
// hop is still the real client — spoofing the left does nothing.
func TestClientIP_RightmostUntrusted(t *testing.T) {
	trusted := cidrs(t, "127.0.0.0/8")
	// Attacker prepends 127.0.0.1; the trusted proxy appended the real peer 198.51.100.7.
	addr, loop := runClientIP(trusted, "127.0.0.1:9", []string{"127.0.0.1, 198.51.100.7"})
	if loop {
		t.Error("prepended loopback must not win over the real rightmost-untrusted hop")
	}
	if addr != "198.51.100.7" {
		t.Errorf("rightmost untrusted hop should win: got %q", addr)
	}
}

// TestClientIP_MalformedXFFIgnored: a trusted proxy forwarding a garbage
// X-Forwarded-For must not let arbitrary header content become RemoteAddr — the
// peer (the proxy) is kept instead, and the address stays a valid IP.
func TestClientIP_MalformedXFFIgnored(t *testing.T) {
	trusted := cidrs(t, "127.0.0.0/8")
	addr, loop := runClientIP(trusted, "127.0.0.1:9", []string{"not-an-ip, still-garbage"})
	if addr != "127.0.0.1" {
		t.Errorf("garbage X-Forwarded-For should fall back to the proxy peer, got %q", addr)
	}
	if !loop {
		t.Error("falling back to the 127.0.0.1 proxy peer should read as loopback (a valid IP, not garbage)")
	}
}

// TestClientIP_DirectLoopbackStillExempt: a direct loopback connection (no proxy,
// no headers) is still loopback — the legitimate single-box case keeps working.
func TestClientIP_DirectLoopbackStillExempt(t *testing.T) {
	if _, loop := runClientIP(nil, "127.0.0.1:6000", nil); !loop {
		t.Error("a direct loopback connection should be loopback")
	}
	// A direct loopback peer that also sends a spoofed XFF: with no trusted
	// proxies the header is ignored, so it stays loopback (it really is local).
	if _, loop := runClientIP(nil, "[::1]:6000", []string{"8.8.8.8"}); !loop {
		t.Error("direct ::1 should remain loopback; untrusted XFF is ignored")
	}
}
