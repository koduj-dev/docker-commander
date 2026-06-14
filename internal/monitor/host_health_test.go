package monitor

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// newHealthMonitor builds a Monitor backed by an in-memory store. It does NOT
// require Docker: recordHealth/fireHostAlert only touch the store, so these
// tests run under -short.
func newHealthMonitor(t *testing.T) (*Monitor, *store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := crypto.New(key)
	st.SetCipher(c)
	ctx := context.Background()
	return New(st, docker.NewManager(st), nil), st, ctx
}

// TestRecordHealthTransitions checks that only state CHANGES fire alerts: the
// first observation is silent, steady state is silent, and each offline/recover
// flip produces exactly one alert event with the right severity.
func TestRecordHealthTransitions(t *testing.T) {
	m, st, ctx := newHealthMonitor(t)
	down := errors.New("dial tcp: connection refused")
	t0 := time.Now()

	// First observation (reachable) — no alert, just seeds state.
	m.recordHealth(1, "prod", nil, t0)
	if evs, _ := st.ListAlertEvents(ctx, 10); len(evs) != 0 {
		t.Fatalf("first observation should not alert, got %d events", len(evs))
	}
	if h := m.HostHealth()[1]; !h.Reachable || !h.Since.Equal(t0) {
		t.Fatalf("seeded health wrong: %+v", h)
	}

	// Goes offline → one critical alert.
	m.recordHealth(1, "prod", down, t0.Add(30*time.Second))
	evs, _ := st.ListAlertEvents(ctx, 10)
	if len(evs) != 1 {
		t.Fatalf("offline transition should alert once, got %d", len(evs))
	}
	if evs[0].Type != "host" || evs[0].Severity != "critical" || evs[0].HostName != "prod" {
		t.Errorf("offline alert wrong: %+v", evs[0])
	}
	if h := m.HostHealth()[1]; h.Reachable || h.Err == "" {
		t.Errorf("health should be unreachable with an error: %+v", h)
	}

	// Still offline → no new alert (steady state), Since unchanged.
	offlineSince := m.HostHealth()[1].Since
	m.recordHealth(1, "prod", down, t0.Add(60*time.Second))
	if evs, _ := st.ListAlertEvents(ctx, 10); len(evs) != 1 {
		t.Errorf("steady offline should not re-alert, got %d", len(evs))
	}
	if !m.HostHealth()[1].Since.Equal(offlineSince) {
		t.Error("Since should not move while the state is unchanged")
	}

	// Recovers → one info alert mentioning it recovered.
	m.recordHealth(1, "prod", nil, t0.Add(90*time.Second))
	evs, _ = st.ListAlertEvents(ctx, 10)
	if len(evs) != 2 {
		t.Fatalf("recover transition should add an alert, got %d", len(evs))
	}
	// ListAlertEvents is newest-first.
	if evs[0].Severity != "info" || evs[0].Type != "host" {
		t.Errorf("recover alert wrong: %+v", evs[0])
	}
	if h := m.HostHealth()[1]; !h.Reachable {
		t.Errorf("health should be reachable again: %+v", h)
	}
}

// TestRecordHealthInitialDownIsSilent guards the "no spurious alert at startup"
// rule: a host that is already unreachable on its first probe must not alert.
func TestRecordHealthInitialDownIsSilent(t *testing.T) {
	m, st, ctx := newHealthMonitor(t)
	m.recordHealth(2, "laptop", errors.New("no route to host"), time.Now())
	if evs, _ := st.ListAlertEvents(ctx, 10); len(evs) != 0 {
		t.Errorf("a host down on first probe must not alert, got %d", len(evs))
	}
	if m.HostHealth()[2].Reachable {
		t.Error("health should record the host as unreachable")
	}
}
