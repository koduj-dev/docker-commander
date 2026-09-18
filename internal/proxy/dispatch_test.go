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
