package monitor

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// right away — as a firing, since nobody was told yet (a repeat is hidden
	// in the feed by default, which made the alert look gone).
	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))

	evs = events(t, st, ctx)
	if len(evs) != 2 {
		t.Fatalf("expected a repeat event immediately once the window ended, got %d:\n%s", len(evs), dump(evs))
	}
	if evs[0].Kind != store.KindFiring || evs[0].Suppressed {
		t.Fatalf("the second event should be the first real delivery, as a firing: %+v", evs[0])
	}

	// Once that was delivered, later announcements really are repeats again.
	st2, _ := st.ListAlertStates(ctx)
	if len(st2) != 1 || st2[0].NotifiedAt.IsZero() {
		t.Fatalf("the delivery should have stamped NotifiedAt: %+v", st2)
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

// captureLog redirects the standard logger for one test — the alert line is
// what lands in the journal/syslog, so that is what is asserted on.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// The process log must say when an alert was silenced: without it an operator
// reading the journal sees "alert kind=firing ..." during maintenance and has
// no way to tell that nobody was paged.
func TestAlertLogLineSaysWhenAMaintenanceWindowSilencedIt(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)
	logs := captureLog(t)

	rule := store.AlertRule{Name: "mem", Type: "resource", Severity: "warning", CooldownSec: 60}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID
	winID, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "es upgrade", Reason: "planned", HostIDs: []int64{hostID},
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	m.fire(ctx, rule, hostID, "host1", "c1", "cms3_es01", "", "memory over 5%", nil)

	line := logs.String()
	for _, want := range []string{"alert kind=firing", `rule="mem"`, `container="cms3_es01"`, "silenced=true", `window_name="es upgrade"`, "maintenance_window=" + itoa(winID)} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %q:\n%s", want, line)
		}
	}
}

func TestAlertLogLineDoesNotClaimSilenceOutsideAWindow(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	logs := captureLog(t)

	rule := store.AlertRule{Name: "mem", Type: "resource", Severity: "warning", CooldownSec: 60}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID

	m.fire(ctx, rule, 1, "host1", "c1", "cms3_es01", "", "memory over 5%", nil)

	line := logs.String()
	if !strings.Contains(line, "alert kind=firing") {
		t.Fatalf("the alert line should still be logged:\n%s", line)
	}
	if strings.Contains(line, "silenced") {
		t.Errorf("no window is active, but the line claims silence:\n%s", line)
	}
}

// A silenced repeat says nothing the firing event didn't, and with many
// containers on one rule it is what would fill the table for a whole window.
func TestSilencedRepeatIsNotStoredButOtherKindsAre(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)
	logs := captureLog(t)

	rule := store.AlertRule{Name: "mem", Type: "resource", Severity: "warning", CooldownSec: 60}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID
	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "w", HostIDs: []int64{hostID}, StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{store.KindFiring, store.KindRepeat, store.KindRepeat, store.KindResolved} {
		if suppressed := m.emit(ctx, rule, hostID, "host1", "c1", "es01", "", "msg", nil, kind, 5); !suppressed {
			t.Fatalf("%s should report suppressed", kind)
		}
	}
	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if len(events) != 2 {
		t.Fatalf("stored kinds = %v, want just firing and resolved (repeats skipped)", kinds)
	}
	// ...but every one still reached the process log, silenced.
	if got := strings.Count(logs.String(), "silenced=true"); got != 4 {
		t.Errorf("%d silenced log lines, want 4 (nothing is lost from the journal):\n%s", got, logs.String())
	}
}

func TestRepeatOutsideAWindowIsStored(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	rule := store.AlertRule{Name: "mem", Type: "resource", Severity: "warning", CooldownSec: 60}
	ruleID, err := st.CreateAlertRule(ctx, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ID = ruleID
	m.emit(ctx, rule, 1, "host1", "c1", "es01", "", "msg", nil, store.KindRepeat, 5)
	events, _, _ := st.ListAlertEvents(ctx, store.AlertQuery{})
	if len(events) != 1 || events[0].Kind != store.KindRepeat {
		t.Fatalf("a repeat with no window active must still be recorded, got %+v", events)
	}
}

// The journal must say when a silence begins and ends — otherwise "alerts are
// being held back" is invisible to anyone reading it.
func TestMaintenanceTransitionsAreLogged(t *testing.T) {
	logs := captureLog(t)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	w := store.MaintenanceWindow{ID: 1, Name: "Test", StartsAt: now.Add(-9 * time.Minute), EndsAt: now.Add(time.Hour)}

	// Startup with a window already running: reported as active, with its real start.
	cur := logMaintenanceTransitions(nil, []store.MaintenanceWindow{w}, now, true)
	if s := logs.String(); !strings.Contains(s, "maintenance window active id=1") || !strings.Contains(s, `name="Test"`) || !strings.Contains(s, "remaining=1h0m0s") || !strings.Contains(s, `scope="everything"`) {
		t.Errorf("startup line wrong:\n%s", s)
	}

	// Unchanged: silent.
	logs.Reset()
	cur = logMaintenanceTransitions(cur, []store.MaintenanceWindow{w}, now.Add(30*time.Second), false)
	if logs.Len() != 0 {
		t.Errorf("no transition, but logged:\n%s", logs.String())
	}

	// A new window starts.
	logs.Reset()
	w2 := store.MaintenanceWindow{ID: 2, Name: "Nightly", Project: "shop", StartsAt: now, EndsAt: now.Add(90 * time.Minute)}
	cur = logMaintenanceTransitions(cur, []store.MaintenanceWindow{w, w2}, now.Add(time.Minute), false)
	if s := logs.String(); !strings.Contains(s, "maintenance window started id=2") || !strings.Contains(s, "duration=1h30m0s") || !strings.Contains(s, "project~shop") {
		t.Errorf("start line wrong:\n%s", s)
	}

	// Window 1 ends by the clock; window 2 is ended early by an operator.
	logs.Reset()
	w2.Ended = true
	at := now.Add(70 * time.Minute)
	cur = logMaintenanceTransitions(cur, []store.MaintenanceWindow{w, w2}, at, false)
	s := logs.String()
	if !strings.Contains(s, "maintenance window ended id=1") || !strings.Contains(s, "maintenance window ended early id=2") || !strings.Contains(s, "alert delivery resumes") {
		t.Errorf("end lines wrong:\n%s", s)
	}

	// A window that vanishes (deleted) is reported as removed.
	logs.Reset()
	w3 := store.MaintenanceWindow{ID: 3, Name: "x", StartsAt: at, EndsAt: at.Add(time.Hour)}
	cur = logMaintenanceTransitions(cur, []store.MaintenanceWindow{w3}, at, false)
	logs.Reset()
	logMaintenanceTransitions(cur, nil, at.Add(time.Minute), false)
	if !strings.Contains(logs.String(), "maintenance window removed id=3") {
		t.Errorf("removal line wrong:\n%s", logs.String())
	}
}
