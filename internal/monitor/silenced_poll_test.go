package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// While a window is active and the condition stays true, polling every few
// seconds must not fill the feed: nothing is owed to anyone until it ends.
func TestSilencedConditionDoesNotEmitOnEveryPoll(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id := addRule(t, st, ctx, "Memory", "warning", 5, "mem", 300)
	cs := busyContainer()
	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "upgrade", Reason: "planned", HostIDs: []int64{cs.HostID}, RuleID: &id,
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.ready(id, cs.ID)
		m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))
	}
	if evs := events(t, st, ctx); len(evs) != 1 {
		t.Fatalf("5 polls inside one window stored %d events, want 1 (the silenced firing):\n%s", len(evs), dump(evs))
	}
}

// A window that was already open when the condition started keeps the
// first-delivery gate closed, so nothing may be recorded per poll — but the
// moment the window ends, the very next poll delivers it (as a firing, since
// nobody was ever told).
func TestSilencedConditionIsDeliveredOnceAfterTheWindowEnds(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id := addRule(t, st, ctx, "Memory", "warning", 5, "mem", 300)
	cs := busyContainer()
	winID, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "upgrade", Reason: "planned", HostIDs: []int64{cs.HostID}, RuleID: &id,
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m.ready(id, cs.ID)
		m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))
	}
	if err := st.EndMaintenanceWindow(ctx, winID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m.ready(id, cs.ID)
		m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))
	}
	evs := events(t, st, ctx)
	if len(evs) != 2 {
		t.Fatalf("want the silenced firing plus ONE delivered firing, got %d:\n%s", len(evs), dump(evs))
	}
	if evs[0].Kind != store.KindFiring || evs[0].Suppressed {
		t.Errorf("newest event should be the first real delivery: %+v", evs[0])
	}
}

// A repeat of something that WAS delivered, silenced by a later window, must be
// re-evaluated once per cooldown — not on every poll.
func TestSilencedRepeatIsNotRetriedEveryPoll(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id := addRule(t, st, ctx, "Memory", "warning", 5, "mem", 300)
	cs := busyContainer()
	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs)) // delivered firing: NotifiedAt = now

	states, _ := st.ListAlertStates(ctx)
	states[0].NotifiedAt = time.Now().Add(-time.Hour) // the cooldown is long over
	if err := st.UpsertAlertState(ctx, &states[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "upgrade", Reason: "planned", HostIDs: []int64{cs.HostID}, RuleID: &id,
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	for i := 0; i < 5; i++ {
		m.ready(id, cs.ID)
		m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))
	}
	if n := strings.Count(logs.String(), "silenced=true"); n != 1 {
		t.Errorf("the silenced repeat was evaluated %d times in 5 polls, want once (then the cooldown holds):\n%s", n, logs.String())
	}
}
