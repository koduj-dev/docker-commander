package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewReverseProxyStripsForgedForwardingHeaders is the regression for a
// P1 a code review caught before merge: httputil.NewSingleHostReverseProxy
// (the deprecated Director shape) preserves whatever X-Forwarded-For/-Host/
// -Proto a CLIENT sends, letting any public client spoof its own apparent
// source IP, host, or scheme to the backend — exactly the trust boundary
// this proxy exists to enforce, since backends commonly trust these headers
// precisely because they expect a reverse proxy in front of them to set
// them honestly.
func TestNewReverseProxyStripsForgedForwardingHeaders(t *testing.T) {
	var gotXFF, gotXFHost, gotXFProto, gotXRealIP string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotXFHost = r.Header.Get("X-Forwarded-Host")
		gotXFProto = r.Header.Get("X-Forwarded-Proto")
		gotXRealIP = r.Header.Get("X-Real-IP")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	rp := newReverseProxy(strings.TrimPrefix(backend.URL, "http://"))

	r := httptest.NewRequest("GET", "http://app.example.com/", nil)
	r.RemoteAddr = "203.0.113.9:54321" // the REAL peer
	// A malicious/forged client trying to spoof its own origin. X-Real-IP is
	// NOT part of the X-Forwarded-* family Go's Rewrite path strips
	// automatically — it needs its own explicit handling (see
	// newReverseProxy), which is exactly what this case pins.
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	r.Header.Set("X-Forwarded-Host", "internal-admin.example.com")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Real-IP", "127.0.0.1")
	w := httptest.NewRecorder()

	rp.ServeHTTP(w, r)

	if gotXFF != "203.0.113.9" {
		t.Errorf("X-Forwarded-For = %q, want the real peer 203.0.113.9 (client-forged value must never survive)", gotXFF)
	}
	if gotXFHost != "app.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want the real inbound Host app.example.com, not the client-forged value", gotXFHost)
	}
	if gotXFProto != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http (the real inbound scheme — r.TLS is nil here), not the client-forged https", gotXFProto)
	}
	if gotXRealIP != "203.0.113.9" {
		t.Errorf("X-Real-IP = %q, want the real peer 203.0.113.9 (client-forged value must never survive) — "+
			"a stock chi middleware.RealIP prefers this header over X-Forwarded-For", gotXRealIP)
	}
}

// TestNewReverseProxyPreservesTheMappedDomainAsHost: the backend should see
// the public domain it was mapped under (app.example.com), not the internal
// 127.0.0.1:<port> dial address — many backends build absolute URLs or do
// their own virtual hosting off the Host header.
func TestNewReverseProxyPreservesTheMappedDomainAsHost(t *testing.T) {
	var gotHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	rp := newReverseProxy(strings.TrimPrefix(backend.URL, "http://"))
	r := httptest.NewRequest("GET", "http://app.example.com/", nil)
	r.Host = "app.example.com"
	w := httptest.NewRecorder()

	rp.ServeHTTP(w, r)

	if gotHost != "app.example.com" {
		t.Errorf("backend saw Host = %q, want the mapped domain app.example.com", gotHost)
	}
}
