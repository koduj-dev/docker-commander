package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestHostsReachabilityField checks that GET /api/hosts surfaces a `reachable`
// field, defaulting to true for a host the monitor hasn't probed (the monitor
// isn't running in this harness, so HostHealth is empty → optimistic default).
func TestHostsReachabilityField(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})

	req, _ := http.NewRequest("GET", a.url+"/api/hosts", nil)
	resp, err := a.c.Do(req)
	if err != nil {
		t.Fatalf("GET /api/hosts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/hosts → %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var hosts []map[string]any
	if err := json.Unmarshal(raw, &hosts); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(hosts) == 0 {
		t.Fatal("expected at least the local host")
	}
	for _, h := range hosts {
		r, ok := h["reachable"]
		if !ok {
			t.Errorf("host %v missing reachable field", h["name"])
			continue
		}
		if r != true {
			t.Errorf("unprobed host %v should default reachable=true, got %v", h["name"], r)
		}
		// An unprobed/healthy host carries no unreachableSince.
		if _, ok := h["unreachableSince"]; ok {
			t.Errorf("reachable host %v should not carry unreachableSince", h["name"])
		}
	}
}
