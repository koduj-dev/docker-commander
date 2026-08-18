package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// PENTEST/regression for CR-002: devCORS must reflect only the known Vite dev
// origins, never an arbitrary Origin — reflecting anything with
// Allow-Credentials: true would let any same-site origin read the API in the
// developer's session.
func TestDevCORS_RejectsUntrustedOrigin(t *testing.T) {
	reached := false
	h := devCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))

	r := httptest.NewRequest("GET", "/api/users", nil)
	r.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("SECURITY: untrusted origin was reflected: %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("SECURITY: credentials allowed for an untrusted origin: %q", got)
	}
	if !reached {
		t.Error("the request itself should still be served, only CORS headers withheld")
	}
}

func TestDevCORS_AllowsViteDevOrigin(t *testing.T) {
	h := devCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	r := httptest.NewRequest("GET", "/api/users", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("want the Vite dev origin allowed, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("want credentials allowed for the trusted dev origin, got %q", got)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("want Vary: Origin so the CORS response isn't cache-poisoned across origins, got %q", got)
	}
}
