package monitor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestEmitSuppressesDeliveryDuringActiveMaintenanceWindow is the point of the
// feature: an active, matching maintenance window must stop the webhook from
// firing, while the event itself is still recorded — "stop the paging, not
// the observing" (NEXT.md). Since emit() returns BEFORE ever calling
// dispatcher.dispatch when suppressed, checking for a delivery record
// immediately after is deterministic, not a race against an async send that
// simply never happens.
func TestEmitSuppressesDeliveryDuringActiveMaintenanceWindow(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)

	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("webhook must not be called while a matching maintenance window is active")
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	rule := store.AlertRule{Name: "cpu", Type: "resource", Severity: "critical", WebhookID: &whID, CooldownSec: 60}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID

	winID, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "upgrade", Reason: "planned", HostIDs: []int64{hostID}, RuleID: &ruleID,
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "cpu high", nil)

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("events: got %d, err=%v", len(events), err)
	}
	ev := events[0]
	if !ev.Suppressed || ev.SuppressedBy != winID {
		t.Fatalf("event should be marked suppressed by window %d, got %+v", winID, ev)
	}
	deliveries, err := st.AlertDeliveriesFor(ctx, []int64{ev.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries[ev.ID]) != 0 {
		t.Fatalf("no delivery should have been attempted, got %+v", deliveries[ev.ID])
	}
}

// TestEmitDeliversWhenNoWindowMatches is the regression guard against a
// suppression check that fails open in the wrong direction — an event with
// no covering window must be delivered exactly as before this feature
// existed.
func TestEmitDeliversWhenNoWindowMatches(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)

	received := make(chan struct{}, 1)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		received <- struct{}{}
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	rule := store.AlertRule{Name: "cpu", Type: "resource", Severity: "critical", WebhookID: &whID, CooldownSec: 60}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID

	// A window that exists but scopes to a DIFFERENT host — must not suppress.
	otherHost := int64(2)
	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "unrelated", Reason: "planned", HostIDs: []int64{otherHost},
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "cpu high", nil)

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook should have been called: no window covers this event")
	}

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 || events[0].Suppressed {
		t.Fatalf("event should not be suppressed: %+v err=%v", events, err)
	}
}

// TestFireHostAlertRespectsHostScopedMaintenanceWindow covers the path with
// no container or rule to scope by (host reachability) — a host-wide window
// must still suppress it, and email delivery must be skipped.
func TestFireHostAlertRespectsHostScopedMaintenanceWindow(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(5)

	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "host maintenance", Reason: "planned reboot", HostIDs: []int64{hostID},
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	m.fireHostAlert(hostID, "host5", false, 0)

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("events: got %d, err=%v", len(events), err)
	}
	if !events[0].Suppressed {
		t.Fatalf("host-down event should be suppressed by the host-scoped window: %+v", events[0])
	}
}

// TestProjectForScopesAMaintenanceWindowByComposeStack proves the compose
// project label read into ContainerStat.Project (populated by pollStats) is
// what a Project-scoped window actually matches against — not just that the
// field exists.
func TestProjectForScopesAMaintenanceWindowByComposeStack(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)
	m.mu.Lock()
	m.snapshot["c1"] = ContainerStat{HostID: hostID, ID: "c1", Name: "web-1", Project: "prod-stack"}
	m.mu.Unlock()

	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("webhook must not be called: the project-scoped window covers this container")
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	rule := store.AlertRule{Name: "cpu", Type: "resource", Severity: "critical", WebhookID: &whID, CooldownSec: 60}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID

	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "stack maintenance", Reason: "planned", Project: "prod",
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "cpu high", nil)

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 || !events[0].Suppressed {
		t.Fatalf("event should be suppressed via the container's compose project: %+v err=%v", events, err)
	}
}

func newMaintenanceMonitor(t *testing.T) (*Monitor, *store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, 32)
	c, _ := crypto.New(key)
	st.SetCipher(c)
	return New(st, nil, nil), st, context.Background()
}
