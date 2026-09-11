package monitor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
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

	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "", "cpu high", nil)

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

	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "", "cpu high", nil)

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

// TestFireUsesTheProjectItWasGivenForWindowScoping proves the project a
// caller passes into fire()/emit() — not any snapshot lookup — is what a
// Project-scoped window actually matches against. emit() deliberately takes
// this as an explicit parameter rather than deriving it from the stats
// snapshot: the snapshot only refreshes once per poll interval, so a
// container created between polls (the exact moment right after a deploy)
// would otherwise resolve to no project at all and slip past a
// project-scoped auto-silence window.
func TestFireUsesTheProjectItWasGivenForWindowScoping(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)

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

	// Note: nothing is populated into m.snapshot — proving this does NOT
	// depend on the stats snapshot, unlike the mechanism this replaced.
	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "prod-stack", "cpu high", nil)

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 || !events[0].Suppressed {
		t.Fatalf("event should be suppressed via the project fire() was given: %+v err=%v", events, err)
	}
}

// TestSuppressedFireDoesNotConsumeTheCooldown is the point of separating
// "recorded/suppressed" from "actually delivered": before this fix, fire()
// stored the cooldown timestamp unconditionally, so a genuine event right
// after a maintenance window ended could be silently dropped — no record,
// no delivery — by a cooldown that only "fired" because of a notification
// nobody received.
func TestSuppressedFireDoesNotConsumeTheCooldown(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)

	received := make(chan struct{}, 1)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	// A long cooldown, deliberately: the test can only pass because the
	// suppressed fire left the cooldown untouched, never because the
	// cooldown itself happened to elapse in real time.
	rule := store.AlertRule{Name: "state", Type: "state", Severity: "critical", WebhookID: &whID, CooldownSec: 3600}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID

	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "upgrade", Reason: "planned", HostIDs: []int64{hostID}, RuleID: &ruleID,
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(1100 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}

	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "", "died", nil)
	time.Sleep(1300 * time.Millisecond) // the window has now ended (RFC3339 storage is second-precision)
	m.fire(ctx, rule, hostID, "host1", "c1", "web-1", "", "died again", nil)

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("a genuine event right after the window ended should have been delivered")
	}

	evs, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected both the suppressed and the delivered event recorded, got %d", len(evs))
	}
}

// TestSuppressedResourceEmitDoesNotAdvanceNotifiedAt is the level-triggered
// half of the same fix. Before it, every emit — suppressed or not — stamped
// NotifiedAt = now, so a condition that started (and was only ever
// suppressed) during a window would wait out a full repeat interval after
// the window ended before resuming delivery, not "immediately" as the UI's
// own End-window confirmation promises.
func TestSuppressedResourceEmitDoesNotAdvanceNotifiedAt(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	// A long cooldown/repeat interval, deliberately: the test can only pass
	// because the suppressed emit left NotifiedAt at zero, never because 300s
	// of real interval genuinely elapsed during the test.
	id := addRule(t, st, ctx, "Memory", "warning", 5, "mem", 300)
	cs := busyContainer()

	winID, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "upgrade", Reason: "planned", HostIDs: []int64{cs.HostID}, RuleID: &id,
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(1100 * time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}

	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 1 || !evs[0].Suppressed || evs[0].SuppressedBy != winID {
		t.Fatalf("the first firing should be recorded, suppressed by the window: %s", dump(evs))
	}

	time.Sleep(1300 * time.Millisecond) // the window has now ended (RFC3339 storage is second-precision)

	// The condition hasn't changed. With a real (delivered) notification,
	// this poll would stay quiet for the rest of the 300s interval — but
	// the only notification so far was suppressed, so it must re-announce
	// as a repeat right away.
	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))

	evs = events(t, st, ctx)
	if len(evs) != 2 {
		t.Fatalf("expected a repeat event immediately once the window ended, got %d:\n%s", len(evs), dump(evs))
	}
	if evs[0].Kind != store.KindRepeat || evs[0].Suppressed {
		t.Fatalf("the second event should be a delivered repeat: %+v", evs[0])
	}
}

// TestHandleEventUsesTheEventsOwnProjectForWindowScoping is the fix for the
// gap left when project scoping was still snapshot-only: a brand-new
// container created moments ago (the exact situation right after a deploy)
// isn't in any stats snapshot yet, but Docker's own event stream already
// carries its compose labels — handleEvent must use THAT, not wait for the
// next poll.
func TestHandleEventUsesTheEventsOwnProjectForWindowScoping(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)

	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("webhook must not be called: the event's own project should have matched the window")
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	rule := store.AlertRule{
		Name: "state", Enabled: true, Type: "state", Config: `{"events":["die"]}`,
		Severity: "critical", WebhookID: &whID,
	}
	if _, err := st.CreateAlertRule(ctx, &rule); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "deploy grace", Reason: "auto", Project: "shop-prod",
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// A brand-new container, deliberately never added to m.snapshot — proving
	// this does not depend on the stats snapshot at all.
	m.handleEvent(ctx, hostID, "host1", docker.Event{
		Action: "die", ContainerID: "c-new", ContainerName: "shop-web-1", Project: "shop-prod",
	})

	evs, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || !evs[0].Suppressed {
		t.Fatalf("a state event for a container absent from any snapshot should still be suppressed via the event's own project: %+v", evs)
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
