package store

import (
	"context"
	"testing"
	"time"
)

func TestMaintenanceWindowActiveOneOff(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	w := MaintenanceWindow{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if !w.Active(now) {
		t.Fatal("one-off window covering now should be active")
	}
	if w.Active(now.Add(-2 * time.Hour)) {
		t.Fatal("one-off window should not be active before its start")
	}
	if w.Active(now.Add(2 * time.Hour)) {
		t.Fatal("one-off window should not be active after its end")
	}
}

func TestMaintenanceWindowEndedIsNeverActive(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	w := MaintenanceWindow{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Ended: true}
	if w.Active(now) {
		t.Fatal("an ended window must never be active, even mid-schedule")
	}
}

func TestMaintenanceWindowRecurringWeekdayAndTime(t *testing.T) {
	// A Sunday 02:00-04:00 UTC weekly window, series starting well in the past.
	w := MaintenanceWindow{
		Recurring: true, StartsAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Weekdays: []time.Weekday{time.Sunday}, TimeOfDay: "02:00", DurationMin: 120,
	}
	// 2026-09-13 is a Sunday.
	inside := time.Date(2026, 9, 13, 3, 0, 0, 0, time.UTC)
	if !w.Active(inside) {
		t.Fatal("recurring window should be active during its Sunday occurrence")
	}
	wrongDay := time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC) // Monday
	if w.Active(wrongDay) {
		t.Fatal("recurring window should not be active on a day it doesn't recur")
	}
	wrongTime := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	if w.Active(wrongTime) {
		t.Fatal("recurring window should not be active outside its time-of-day slot")
	}
	beforeSeries := time.Date(2025, 1, 4, 3, 0, 0, 0, time.UTC) // a Sunday before StartsAt
	if w.Active(beforeSeries) {
		t.Fatal("recurring window should not be active before its series starts")
	}
}

// TestMaintenanceWindowRecurringSpansMidnight is the point of checking both
// today's and yesterday's occurrence: a window starting late in the evening
// for long enough to cross into the next calendar day must still be found
// active just after midnight, when the covering occurrence "belongs" to the
// previous day's weekday.
func TestMaintenanceWindowRecurringSpansMidnight(t *testing.T) {
	w := MaintenanceWindow{
		Recurring: true, StartsAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Weekdays: []time.Weekday{time.Saturday}, TimeOfDay: "23:00", DurationMin: 120,
	}
	// 2026-09-12 is a Saturday; the occurrence runs 23:00 Sat -> 01:00 Sun.
	justAfterMidnight := time.Date(2026, 9, 13, 0, 30, 0, 0, time.UTC)
	if !w.Active(justAfterMidnight) {
		t.Fatal("a midnight-spanning recurring occurrence should still be active just after midnight")
	}
	wellIntoSunday := time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
	if w.Active(wellIntoSunday) {
		t.Fatal("the occurrence should have ended by 02:00, well past its duration")
	}
}

func TestMaintenanceWindowRecurringSeriesEndsAt(t *testing.T) {
	w := MaintenanceWindow{
		Recurring: true, StartsAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Weekdays: []time.Weekday{time.Sunday}, TimeOfDay: "02:00", DurationMin: 120,
	}
	// A Sunday that would otherwise match, but after the series' own EndsAt.
	afterSeriesEnd := time.Date(2026, 9, 13, 3, 0, 0, 0, time.UTC)
	if w.Active(afterSeriesEnd) {
		t.Fatal("a recurring series should stop producing occurrences after its own EndsAt")
	}
}

func TestMaintenanceWindowRecurringTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata not available: %v", err)
	}
	w := MaintenanceWindow{
		Recurring: true, StartsAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Weekdays: []time.Weekday{time.Sunday}, TimeOfDay: "02:00", DurationMin: 60, Timezone: "America/New_York",
	}
	// 2026-09-13 02:30 America/New_York — well outside DST transitions.
	local := time.Date(2026, 9, 13, 2, 30, 0, 0, loc)
	if !w.Active(local.In(time.UTC)) {
		t.Fatal("recurring window should evaluate its time-of-day in its own timezone, not UTC")
	}
	// The same instant read as if it were 02:30 UTC falls outside the slot,
	// proving the timezone is actually applied rather than ignored.
	sameClockTimeUTC := time.Date(2026, 9, 13, 2, 30, 0, 0, time.UTC)
	if w.Active(sameClockTimeUTC) {
		t.Fatal("test setup: 02:30 UTC should not coincide with 02:00-03:00 America/New_York")
	}
}

func TestMaintenanceWindowMatchesScope(t *testing.T) {
	ruleID := int64(7)
	w := MaintenanceWindow{
		HostIDs: []int64{1, 2}, Project: "prod", Container: "web", RuleID: &ruleID, Severities: []string{"warning", "critical"},
	}
	if !w.Matches(1, "prod-stack", "web-1", 7, "critical") {
		t.Fatal("a fully matching event should match")
	}
	if w.Matches(3, "prod-stack", "web-1", 7, "critical") {
		t.Fatal("a host outside HostIDs should not match")
	}
	if w.Matches(1, "staging-stack", "web-1", 7, "critical") {
		t.Fatal("a project not containing the substring should not match")
	}
	if w.Matches(1, "prod-stack", "db-1", 7, "critical") {
		t.Fatal("a container not containing the substring should not match")
	}
	if w.Matches(1, "prod-stack", "web-1", 9, "critical") {
		t.Fatal("a different rule id should not match")
	}
	if w.Matches(1, "prod-stack", "web-1", 7, "info") {
		t.Fatal("a severity outside Severities should not match")
	}
}

func TestMaintenanceWindowMatchesEmptyScopeMeansEverything(t *testing.T) {
	w := MaintenanceWindow{}
	if !w.Matches(999, "anything", "anything", 999, "critical") {
		t.Fatal("a window with no scope set should match every event, mirroring an empty rule Target")
	}
}

func maintenanceStore(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	uid, err := st.CreateUser(context.Background(), &User{Username: "opsuser", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return st, uid
}

func TestMaintenanceWindowCRUD(t *testing.T) {
	st, uid := maintenanceStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	id, err := st.CreateMaintenanceWindow(ctx, &MaintenanceWindow{
		Name: "DB upgrade", Reason: "planned Postgres major version bump", AuthorID: uid,
		HostIDs: []int64{1}, Container: "db", Severities: []string{"warning", "critical"},
		StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	all, err := st.ListMaintenanceWindows(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: got %d windows, err=%v", len(all), err)
	}
	got := all[0]
	if got.Name != "DB upgrade" || got.Author != "opsuser" || len(got.HostIDs) != 1 || got.HostIDs[0] != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Severities) != 2 {
		t.Fatalf("severities round-trip: %+v", got.Severities)
	}

	found, err := st.FindActiveMaintenanceWindow(ctx, 1, "", "db-primary", 0, "critical", now)
	if err != nil || found == nil {
		t.Fatalf("expected an active matching window, got %v err=%v", found, err)
	}
	if found.ID != id {
		t.Fatalf("wrong window matched: %d", found.ID)
	}

	if w, err := st.FindActiveMaintenanceWindow(ctx, 2, "", "db-primary", 0, "critical", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if w != nil {
		t.Fatal("a host outside scope must not match")
	}

	if err := st.UpdateMaintenanceWindow(ctx, id, &MaintenanceWindow{
		Name: "DB upgrade (extended)", Reason: "ran long", AuthorID: uid,
		StartsAt: now.Add(-time.Minute), EndsAt: now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	updated, err := st.MaintenanceWindowByID(ctx, id)
	if err != nil || updated.Name != "DB upgrade (extended)" {
		t.Fatalf("update did not persist: %+v err=%v", updated, err)
	}
	// Update clears scope not resent — proves it's a full replace, not a merge.
	if len(updated.HostIDs) != 0 || len(updated.Severities) != 0 {
		t.Fatalf("update should replace scope wholesale, got %+v", updated)
	}

	if err := st.EndMaintenanceWindow(ctx, id); err != nil {
		t.Fatalf("end: %v", err)
	}
	ended, err := st.MaintenanceWindowByID(ctx, id)
	if err != nil || !ended.Ended {
		t.Fatalf("window should be ended: %+v err=%v", ended, err)
	}
	if w, _ := st.FindActiveMaintenanceWindow(ctx, 1, "", "db-primary", 0, "critical", now); w != nil {
		t.Fatal("an ended window must not be found active")
	}

	if err := st.DeleteMaintenanceWindow(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.MaintenanceWindowByID(ctx, id); err == nil {
		t.Fatal("deleted window should no longer be found")
	}
}

func TestMaintenanceWindowUpdateEndDeleteUnknownIDReportsNotFound(t *testing.T) {
	st, uid := maintenanceStore(t)
	ctx := context.Background()
	_ = uid

	if err := st.UpdateMaintenanceWindow(ctx, 999, &MaintenanceWindow{Name: "x", StartsAt: time.Now()}); err != ErrNotFound {
		t.Fatalf("update of unknown id: want ErrNotFound, got %v", err)
	}
	if err := st.EndMaintenanceWindow(ctx, 999); err != ErrNotFound {
		t.Fatalf("end of unknown id: want ErrNotFound, got %v", err)
	}
	if err := st.DeleteMaintenanceWindow(ctx, 999); err != ErrNotFound {
		t.Fatalf("delete of unknown id: want ErrNotFound, got %v", err)
	}
}

// TestMaintenanceWindowIncidentStillRecordedWhenSuppressed pins the design
// requirement from NEXT.md: a silence stops paging, not observing. It is
// exercised at the monitor layer (internal/monitor), not here — this store
// test only confirms AlertEvent carries the fields that make that possible.
func TestMaintenanceWindowSuppressedFieldsRoundTrip(t *testing.T) {
	st, _ := maintenanceStore(t)
	ctx := context.Background()
	id, err := st.InsertAlertEvent(ctx, &AlertEvent{
		RuleName: "cpu", Severity: "warning", HostID: 1, ContainerName: "web-1",
		Suppressed: true, SuppressedBy: 42,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	events, _, err := st.ListAlertEvents(ctx, AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("list: got %d, err=%v", len(events), err)
	}
	if !events[0].Suppressed || events[0].SuppressedBy != 42 {
		t.Fatalf("suppressed fields did not round-trip: %+v", events[0])
	}
	if events[0].ID != id {
		t.Fatalf("wrong event id")
	}
}
