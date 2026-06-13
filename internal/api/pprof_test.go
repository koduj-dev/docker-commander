package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPProfHandler checks the profiling endpoints are served. Access control is
// NOT this handler's job — it is enforced by binding it to a loopback-only
// listener in cmd/dockercmd (gating by client IP on the main router would be
// unsafe behind chi's spoofable RealIP middleware), so there is nothing for this
// handler to reject.
func TestPProfHandler(t *testing.T) {
	ts := httptest.NewServer(PProfHandler())
	defer ts.Close()

	for _, path := range []string{
		"/debug/pprof/",
		"/debug/pprof/heap?debug=1",
		"/debug/pprof/goroutine?debug=1",
		"/debug/pprof/cmdline",
	} {
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
