package proxy

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func stubHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
}

func newDispatchTestProxy(t *testing.T, mappedDomain string, localProject bool) *Proxy {
	t.Helper()
	st, ctx := newTestStore(t)
	hostID := int64(0)
	if !localProject {
		var err error
		hostID, err = st.CreateHost(ctx, &store.Host{Name: "remote", Kind: "ssh", Address: "10.0.0.9"})
		if err != nil {
			t.Fatal(err)
		}
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "App", Slug: "app", HostID: hostID, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if mappedDomain != "" {
		if _, err := st.CreateDomainMapping(ctx, pid, mappedDomain, "web", 9999, "acme", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	return newTestProxy(t, st)
}

func TestCombinedHandlerRoutesAdminDomainToAdminHandler(t *testing.T) {
	p := newDispatchTestProxy(t, "", true)
	h := CombinedHandler(stubHandler("admin"), []string{"admin.example.com"}, p)

	r := httptest.NewRequest("GET", "https://admin.example.com/", nil)
	r.Host = "admin.example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Body.String() != "admin" {
		t.Errorf("body = %q, want the admin handler's response", w.Body.String())
	}
}

// TestCombinedHandlerAdminDomainMatchIsCaseInsensitive is the regression
// for a P2 a code review caught before merge: DNS names are case
// insensitive, but the dispatch's admin-domain set used to do a plain exact
// string match. An operator configuring DC_ACME_DOMAINS with any uppercase
// letter — nothing stops that — would misroute perfectly ordinary lowercase
// admin traffic to the proxy branch, which then refuses it for the wrong
// reason (casing, not eligibility). Covers both directions: mixed-case
// config against a lowercase request, and mixed-case SNI+Host together
// (which must still pass the SNI/Host equality guard AND match the set).
func TestCombinedHandlerAdminDomainMatchIsCaseInsensitive(t *testing.T) {
	p := newDispatchTestProxy(t, "", true)
	h := CombinedHandler(stubHandler("admin"), []string{"Admin.Example.com"}, p)

	t.Run("mixed-case config, lowercase request", func(t *testing.T) {
		r := httptest.NewRequest("GET", "https://admin.example.com/", nil)
		r.Host = "admin.example.com"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Body.String() != "admin" {
			t.Errorf("body = %q (code %d), want the admin handler's response", w.Body.String(), w.Code)
		}
	})

	t.Run("mixed-case SNI and Host together", func(t *testing.T) {
		r := httptest.NewRequest("GET", "https://ADMIN.EXAMPLE.COM/", nil)
		r.Host = "ADMIN.EXAMPLE.COM"
		r.TLS = &tls.ConnectionState{ServerName: "ADMIN.EXAMPLE.COM"}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Body.String() != "admin" {
			t.Errorf("body = %q (code %d), want the admin handler's response", w.Body.String(), w.Code)
		}
	})
}

// TestCombinedHandlerAdminDomainMatchHandlesIDNA is the regression for a P2
// a code review caught before merge: plain strings.ToLower matches ASCII
// casing but not IDNA equivalence. autocert.Manager itself canonicalizes via
// idna.Lookup.ToASCII (which ALSO folds case, but goes further — Unicode to
// Punycode). An admin domain configured as literal Unicode
// ("bücher.example") must still match the Punycode A-label
// ("xn--bcher-kva.example") a real client's TLS/HTTP stack actually sends
// on the wire — the two are the same hostname, and the admin manager
// already accepted exactly this equivalence before this dispatch wrapper
// was introduced.
func TestCombinedHandlerAdminDomainMatchHandlesIDNA(t *testing.T) {
	p := newDispatchTestProxy(t, "", true)
	h := CombinedHandler(stubHandler("admin"), []string{"bücher.example"}, p)

	r := httptest.NewRequest("GET", "https://xn--bcher-kva.example/", nil)
	r.Host = "xn--bcher-kva.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Body.String() != "admin" {
		t.Errorf("body = %q (code %d), want the admin handler's response for the Punycode wire form of a Unicode-configured domain",
			w.Body.String(), w.Code)
	}
}

// The SNI/Host consistency check must canonicalize BOTH sides (P2, third
// review): autocert accepts Unicode SNI "bücher.example" and canonicalizes it,
// so a client presenting it with the equivalent Punycode Host denotes ONE name
// and must not be rejected as a mismatch. A genuinely different Host still is.
func TestCombinedHandlerSNIHostCheckIsIDNAAware(t *testing.T) {
	p := newDispatchTestProxy(t, "", true)
	h := CombinedHandler(stubHandler("admin"), []string{"bücher.example"}, p)

	for _, tc := range []struct {
		name, sni, host string
		wantCode        int
	}{
		{"unicode SNI, punycode Host", "bücher.example", "xn--bcher-kva.example", 200},
		{"punycode SNI, unicode Host", "xn--bcher-kva.example", "bücher.example", 200},
		{"different name still rejected", "bücher.example", "other.example", http.StatusMisdirectedRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://x/", nil)
			r.Host = tc.host
			r.TLS = &tls.ConnectionState{ServerName: tc.sni}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}

func TestCombinedGetCertificateAdminDomainMatchIsCaseInsensitive(t *testing.T) {
	admin := &fakeCertGetter{cert: &tls.Certificate{}}
	proxyMgr := &fakeCertGetter{cert: &tls.Certificate{}}
	get := CombinedGetCertificate(admin, proxyMgr, []string{"Admin.Example.com"})

	if _, err := get(&tls.ClientHelloInfo{ServerName: "admin.example.com"}); err != nil {
		t.Fatal(err)
	}
	if !admin.called || proxyMgr.called {
		t.Errorf("a lowercase SNI should still match a mixed-case configured admin domain, got admin.called=%v proxyMgr.called=%v",
			admin.called, proxyMgr.called)
	}
}

func TestCombinedHandlerFallsBackTo404NotAdmin(t *testing.T) {
	// The mutation-worthy case: a mapping that either never existed or was
	// deleted must 404, NEVER silently serve the admin handler — a test that
	// only checked "some response came back" would pass even if this
	// regressed to serving the admin login UI on a stray public domain.
	p := newDispatchTestProxy(t, "", true) // no mapping created at all
	h := CombinedHandler(stubHandler("admin"), []string{"admin.example.com"}, p)

	r := httptest.NewRequest("GET", "https://stray.example.com/", nil)
	r.Host = "stray.example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", w.Code)
	}
	if w.Body.String() == "admin" {
		t.Fatal("an unmatched Host must never fall through to the admin handler")
	}
}

func TestCombinedHandlerRoutesALiveMappingToTheProxy(t *testing.T) {
	// The mapped project's own service isn't actually running (no docker
	// manager wired), so this exercises TryServeHTTP's "known mapping, no
	// live backend" branch — 502, not 404 and not the admin handler. That
	// alone proves dispatch correctly identified app.example.com as a proxy
	// domain rather than falling through.
	p := newDispatchTestProxy(t, "app.example.com", true)
	h := CombinedHandler(stubHandler("admin"), []string{"admin.example.com"}, p)

	r := httptest.NewRequest("GET", "https://app.example.com/", nil)
	r.Host = "app.example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502 (known mapping, no live backend)", w.Code)
	}
	if w.Body.String() == "admin" {
		t.Fatal("a mapped domain must never be routed to the admin handler")
	}
}

func TestCombinedHandlerRejectsSNIHostMismatch(t *testing.T) {
	p := newDispatchTestProxy(t, "app.example.com", true)
	h := CombinedHandler(stubHandler("admin"), []string{"admin.example.com"}, p)

	cases := []struct {
		name, sni, host string
	}{
		{"admin SNI, mapped Host", "admin.example.com", "app.example.com"},
		{"mapped SNI, admin Host", "app.example.com", "admin.example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://"+c.host+"/", nil)
			r.Host = c.host
			r.TLS = &tls.ConnectionState{ServerName: c.sni}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusMisdirectedRequest {
				t.Errorf("code = %d, want 421 misdirected request", w.Code)
			}
			if w.Body.String() == "admin" {
				t.Error("an SNI/Host mismatch must never reach the admin handler")
			}
		})
	}
}

// fakeCertGetter lets CombinedGetCertificate be tested without a real
// autocert.Manager (which would touch a cache directory, and on first real
// use, the network).
type fakeCertGetter struct {
	called bool
	cert   *tls.Certificate
	err    error
}

func (f *fakeCertGetter) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	f.called = true
	return f.cert, f.err
}

func TestCombinedGetCertificateDelegatesBySNI(t *testing.T) {
	admin := &fakeCertGetter{cert: &tls.Certificate{}}
	proxyMgr := &fakeCertGetter{cert: &tls.Certificate{}}
	get := CombinedGetCertificate(admin, proxyMgr, []string{"admin.example.com"})

	if _, err := get(&tls.ClientHelloInfo{ServerName: "admin.example.com"}); err != nil {
		t.Fatal(err)
	}
	if !admin.called || proxyMgr.called {
		t.Errorf("admin SNI should delegate to adminMgr only, got admin.called=%v proxyMgr.called=%v", admin.called, proxyMgr.called)
	}

	admin.called, proxyMgr.called = false, false
	if _, err := get(&tls.ClientHelloInfo{ServerName: "app.example.com"}); err != nil {
		t.Fatal(err)
	}
	if admin.called || !proxyMgr.called {
		t.Errorf("non-admin SNI should delegate to proxyMgr only, got admin.called=%v proxyMgr.called=%v", admin.called, proxyMgr.called)
	}
}
