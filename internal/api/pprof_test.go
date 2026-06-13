package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestLoopbackOnly is the security gate for pprof: only loopback clients pass;
// everything else gets a 404 (not 403 — the path's existence isn't advertised).
func TestLoopbackOnly(t *testing.T) {
	var reached bool
	h := loopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		remote string
		pass   bool
	}{
		{"127.0.0.1:5000", true},
		{"[::1]:5000", true},
		{"10.0.0.4:5000", false},
		{"192.168.1.10:5000", false},
		{"8.8.8.8:5000", false},
	}
	for _, c := range cases {
		reached = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/debug/pprof/heap", nil)
		req.RemoteAddr = c.remote
		h.ServeHTTP(rec, req)
		if c.pass {
			if rec.Code != http.StatusOK || !reached {
				t.Errorf("%s should reach the handler, got %d reached=%v", c.remote, rec.Code, reached)
			}
		} else {
			if rec.Code != http.StatusNotFound || reached {
				t.Errorf("%s should be 404 and not reach the handler, got %d reached=%v", c.remote, rec.Code, reached)
			}
		}
	}
}

// TestMountPProf checks the endpoints are served when mounted (over a loopback
// httptest connection). mountPProf uses no Server fields, so a zero-value Server
// is enough.
func TestMountPProf(t *testing.T) {
	r := chi.NewRouter()
	(&Server{}).mountPProf(r)
	ts := httptest.NewServer(r) // httptest dials 127.0.0.1 → loopback
	defer ts.Close()

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutine?debug=1"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s → %d (want 200)", path, resp.StatusCode)
		}
	}
}
