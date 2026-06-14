package monitor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestHostHealthAgainstFakeDaemon drives the *real* reachability path against a
// stand-in Docker daemon (an HTTP server that answers /_ping). It exercises
// docker.Manager.Ping → recordHealth → fireHostAlert → store, end-to-end, rather
// than calling recordHealth with synthetic data: a reachable host then made
// unreachable must produce exactly one critical "host" alert.
func TestHostHealthAgainstFakeDaemon(t *testing.T) {
	m, st, ctx := newHealthMonitor(t)

	// Minimal fake Docker daemon: any request gets a 200 with the version
	// headers the client expects, so cli.Ping() succeeds.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.45")
		w.Header().Set("Ostype", "linux")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{}")
	}))

	// Register it as a TCP host (tcp:// so the docker client dials it directly).
	addr := "tcp://" + strings.TrimPrefix(fake.URL, "http://")
	hid, err := st.CreateHost(ctx, &store.Host{Name: "fake", Kind: "tcp", Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	// First sweep: the fake answers → reachable. First observation never alerts.
	m.checkAllHosts(ctx)
	if h := m.HostHealth()[hid]; !h.Reachable {
		t.Fatalf("host should be reachable while the fake daemon is up: %+v (err=%q)", h, h.Err)
	}
	if evs, _ := st.ListAlertEvents(ctx, 10); len(evs) != 0 {
		t.Fatalf("first reachable observation must not alert, got %d", len(evs))
	}

	// Take the daemon down, then sweep again: ping fails → offline transition.
	fake.Close()
	m.checkAllHosts(ctx)
	if h := m.HostHealth()[hid]; h.Reachable {
		t.Fatalf("host should be unreachable after the fake daemon is closed: %+v", h)
	}
	evs, _ := st.ListAlertEvents(ctx, 10)
	if len(evs) != 1 {
		t.Fatalf("offline transition should fire exactly one alert, got %d", len(evs))
	}
	if evs[0].Type != "host" || evs[0].Severity != "critical" || evs[0].HostName != "fake" {
		t.Errorf("offline alert wrong: %+v", evs[0])
	}
	if !strings.Contains(evs[0].Message, "unreachable") {
		t.Errorf("offline alert message = %q, want it to mention unreachable", evs[0].Message)
	}
}
