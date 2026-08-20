package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func newIgnoredCVEsTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{cfg: config.Config{}, store: st}, st
}

func TestIgnoredCVEs_ListEmpty(t *testing.T) {
	srv, _ := newIgnoredCVEsTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/images/ignored-cves", nil)
	w := httptest.NewRecorder()
	srv.handleListIgnoredCVEs(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("body = %q, want an empty JSON array (not null)", w.Body.String())
	}
}

func TestIgnoredCVEs_IgnoreThenListThenUnignore(t *testing.T) {
	srv, _ := newIgnoredCVEsTestServer(t)

	// Ignore two CVEs with a reason.
	body := `{"ids":["CVE-2024-1111","CVE-2024-2222"],"reason":"reviewed, accepted"}`
	r := httptest.NewRequest(http.MethodPost, "/api/images/ignored-cves", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleIgnoreCVEs(w, r)
	if w.Code != 200 {
		t.Fatalf("ignore: status = %d, body = %s", w.Code, w.Body.String())
	}

	// List should now show both.
	r = httptest.NewRequest(http.MethodGet, "/api/images/ignored-cves", nil)
	w = httptest.NewRecorder()
	srv.handleListIgnoredCVEs(w, r)
	if !strings.Contains(w.Body.String(), "CVE-2024-1111") || !strings.Contains(w.Body.String(), "CVE-2024-2222") {
		t.Fatalf("list after ignore = %s, want both CVEs", w.Body.String())
	}

	// Unignore one.
	r = httptest.NewRequest(http.MethodDelete, "/api/images/ignored-cves/CVE-2024-1111", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "CVE-2024-1111")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	srv.handleUnignoreCVE(w, r)
	if w.Code != 200 {
		t.Fatalf("unignore: status = %d, body = %s", w.Code, w.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, "/api/images/ignored-cves", nil)
	w = httptest.NewRecorder()
	srv.handleListIgnoredCVEs(w, r)
	if strings.Contains(w.Body.String(), "CVE-2024-1111") {
		t.Errorf("list after unignore still contains CVE-2024-1111: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CVE-2024-2222") {
		t.Errorf("list after unignore should still contain CVE-2024-2222: %s", w.Body.String())
	}
}

func TestIgnoredCVEs_EmptyIDsRejected(t *testing.T) {
	srv, _ := newIgnoredCVEsTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/images/ignored-cves", strings.NewReader(`{"ids":[]}`))
	w := httptest.NewRecorder()
	srv.handleIgnoreCVEs(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an empty ids list", w.Code)
	}
}

// PENTEST: a request naming more than maxIgnoreCVEsPerRequest ids is refused
// outright, not silently truncated to the first N — silent truncation would
// let an oversized submission look like it succeeded.
func TestPentestIgnoredCVEs_TooManyIDsRefused(t *testing.T) {
	srv, _ := newIgnoredCVEsTestServer(t)
	ids := make([]string, maxIgnoreCVEsPerRequest+1)
	for i := range ids {
		ids[i] = `"CVE-2024-0001"`
	}
	body := `{"ids":[` + strings.Join(ids, ",") + `]}`
	r := httptest.NewRequest(http.MethodPost, "/api/images/ignored-cves", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIgnoreCVEs(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an oversized ids list", w.Code)
	}
}
