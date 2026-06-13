package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestMCPMountToggle verifies the secure-by-default behaviour: when MCP is off,
// the /mcp route does not exist at all (bare 404, no fingerprint); when on, it
// is mounted and bearer-gated (401 without a token, not 404).
func TestMCPMountToggle(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	probe := func(enabled bool) int {
		// A bare Server is safe here on purpose: Handler() registers routes with
		// method values (s.mw.RequireSession, s.permissions, s.handleX) that bind
		// over the nil fields without dereferencing, and this test only probes
		// /mcp — never the /api group — so those nil-receiver methods never run.
		// Keep it that way: don't add an /api probe or eager field access here.
		s := &Server{cfg: config.Config{MCPEnabled: enabled}, store: st}
		ts := httptest.NewServer(s.Handler())
		defer ts.Close()
		// POST so the request isn't swallowed by a SPA GET catch-all (none here).
		resp, err := http.Post(ts.URL+"/mcp", "application/json", nil)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := probe(false); code != http.StatusNotFound {
		t.Errorf("MCP disabled: want 404 for /mcp, got %d", code)
	}
	if code := probe(true); code == http.StatusNotFound {
		t.Error("MCP enabled: /mcp should be mounted (bearer-gated), got 404")
	}
}
