package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

var retNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

func retAgo(days int) string {
	return retNow.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
}

func retStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, context.Background()
}

func mustExec(t *testing.T, st *Store, q string, args ...any) {
	t.Helper()
	if _, err := st.db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func count(t *testing.T, st *Store, table string) int64 {
	t.Helper()
	var n int64
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRetentionPolicyValidate(t *testing.T) {
	ok := func(p RetentionPolicy) {
		t.Helper()
		if err := p.Validate(); err != nil {
			t.Errorf("%+v should be valid: %v", p, err)
		}
	}
	bad := func(name string, p RetentionPolicy) {
		t.Helper()
		if err := p.Validate(); err == nil {
			t.Errorf("%s: %+v should be rejected", name, p)
		}
	}
	ok(DefaultRetention())
	ok(RetentionPolicy{}) // keep everything
	ok(RetentionPolicy{AuditDays: 30, AlertEventsDays: 1, AlertDeliveriesDays: 1, RevisionsKeep: 3})
	bad("audit below the floor", RetentionPolicy{AuditDays: 29})
	bad("audit negative", RetentionPolicy{AuditDays: -1})
	bad("events negative", RetentionPolicy{AlertEventsDays: -5})
	bad("revisions below the floor", RetentionPolicy{RevisionsKeep: 2})
	bad("absurd TTL", RetentionPolicy{AuditDays: MaxRetentionDays + 1})
	bad("deliveries outlive events", RetentionPolicy{AlertEventsDays: 30, AlertDeliveriesDays: 60})
	bad("deliveries forever, events not", RetentionPolicy{AlertEventsDays: 30, AlertDeliveriesDays: 0})
	ok(RetentionPolicy{AlertEventsDays: 0, AlertDeliveriesDays: 60}) // events kept forever: deliveries may be shorter
}

func TestRetentionDefaultsAndPersistence(t *testing.T) {
	st, ctx := retStore(t)
	got, err := st.Retention(ctx)
	if err != nil || got != DefaultRetention() {
		t.Fatalf("a fresh install gets the defaults (ON), got %+v err=%v", got, err)
	}
	want := RetentionPolicy{AlertEventsDays: 30, AlertDeliveriesDays: 14, AuditDays: 90, RevisionsKeep: 10}
	if err := st.SetRetention(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Retention(ctx); got != want {
		t.Errorf("round trip: got %+v want %+v", got, want)
	}
	if err := st.SetRetention(ctx, RetentionPolicy{AuditDays: 7}); err == nil {
		t.Error("SetRetention must refuse an audit TTL below the floor")
	}
	if got, _ := st.Retention(ctx); got != want {
		t.Errorf("a refused save must not change the stored policy, got %+v", got)
	}
	// A corrupt or invalid stored value falls back to the defaults rather than
	// silently purging with something nobody chose.
	mustExec(t, st, `UPDATE settings SET value = '{"auditDays":1}' WHERE key = ?`, retentionSettingKey)
	if got, _ := st.Retention(ctx); got != DefaultRetention() {
		t.Errorf("an invalid stored policy must fall back to the defaults, got %+v", got)
	}
}

func TestPurgeAlertEventsTakesDeliveriesAndRetriesWithIt(t *testing.T) {
	st, ctx := retStore(t)
	mustExec(t, st, `INSERT INTO alert_events (id, created_at) VALUES (1, ?), (2, ?)`, retAgo(100), retAgo(10))
	mustExec(t, st, `INSERT INTO alert_deliveries (event_id, attempted_at) VALUES (1, ?), (2, ?)`, retAgo(100), retAgo(10))
	mustExec(t, st, `INSERT INTO alert_delivery_retries (event_id, channel, next_attempt_at, created_at) VALUES (1, 'webhook', ?, ?), (2, 'webhook', ?, ?)`,
		retAgo(100), retAgo(100), retAgo(10), retAgo(10))

	ev, del, err := st.PurgeAlertEvents(ctx, 90, retNow)
	if err != nil {
		t.Fatal(err)
	}
	if ev != 1 || del != 1 {
		t.Errorf("deleted events=%d deliveries=%d, want 1 and 1", ev, del)
	}
	if count(t, st, "alert_events") != 1 || count(t, st, "alert_deliveries") != 1 || count(t, st, "alert_delivery_retries") != 1 {
		t.Errorf("the recent event and its rows must survive: events=%d deliveries=%d retries=%d",
			count(t, st, "alert_events"), count(t, st, "alert_deliveries"), count(t, st, "alert_delivery_retries"))
	}
}

func TestPurgeAlertDeliveriesByAge(t *testing.T) {
	st, ctx := retStore(t)
	mustExec(t, st, `INSERT INTO alert_events (id, created_at) VALUES (1, ?)`, retAgo(5))
	mustExec(t, st, `INSERT INTO alert_deliveries (event_id, attempted_at) VALUES (1, ?), (1, ?)`, retAgo(40), retAgo(2))
	n, err := st.PurgeAlertDeliveries(ctx, 30, retNow)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d err=%v, want 1", n, err)
	}
	if count(t, st, "alert_events") != 1 {
		t.Error("purging deliveries must not touch the event")
	}
}

func TestPurgeZeroMeansKeepForever(t *testing.T) {
	st, ctx := retStore(t)
	mustExec(t, st, `INSERT INTO alert_events (id, created_at) VALUES (1, ?)`, retAgo(5000))
	mustExec(t, st, `INSERT INTO alert_deliveries (event_id, attempted_at) VALUES (1, ?)`, retAgo(5000))
	mustExec(t, st, `INSERT INTO audit_log (action, created_at) VALUES ('x', ?)`, retAgo(5000))
	if ev, del, err := st.PurgeAlertEvents(ctx, 0, retNow); ev+del != 0 || err != nil {
		t.Errorf("0 days must keep events: %d %d %v", ev, del, err)
	}
	if n, err := st.PurgeAlertDeliveries(ctx, 0, retNow); n != 0 || err != nil {
		t.Errorf("0 days must keep deliveries: %d %v", n, err)
	}
	if n, err := st.PurgeAudit(ctx, 0, retNow); n != 0 || err != nil {
		t.Errorf("0 days must keep the audit log: %d %v", n, err)
	}
	if count(t, st, "alert_events")+count(t, st, "alert_deliveries")+count(t, st, "audit_log") != 3 {
		t.Error("nothing should have been deleted")
	}
}

func TestPurgeAuditRefusesBelowTheFloor(t *testing.T) {
	st, ctx := retStore(t)
	mustExec(t, st, `INSERT INTO audit_log (action, created_at) VALUES ('recent', ?), ('old', ?)`, retAgo(3), retAgo(400))
	if _, err := st.PurgeAudit(ctx, 1, retNow); err == nil {
		t.Fatal("a 1-day audit TTL must be refused even when called directly")
	}
	if count(t, st, "audit_log") != 2 {
		t.Fatal("a refused purge must delete nothing")
	}
	n, err := st.PurgeAudit(ctx, 365, retNow)
	if err != nil || n != 1 || count(t, st, "audit_log") != 1 {
		t.Errorf("365 days: deleted %d err=%v left=%d, want 1 deleted and the recent entry kept", n, err, count(t, st, "audit_log"))
	}
}

func TestPurgeRunsInBatches(t *testing.T) {
	st, ctx := retStore(t)
	total := purgeBatch*2 + 137
	tx, _ := st.db.Begin()
	for i := 0; i < total; i++ {
		if _, err := tx.Exec(`INSERT INTO audit_log (action, created_at) VALUES ('x', ?)`, retAgo(500)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	n, err := st.PurgeAudit(ctx, 365, retNow)
	if err != nil || n != int64(total) || count(t, st, "audit_log") != 0 {
		t.Errorf("deleted %d of %d err=%v left=%d — a purge must loop past one batch", n, total, err, count(t, st, "audit_log"))
	}
}

func TestPurgeStopsWhenContextIsCancelled(t *testing.T) {
	st, _ := retStore(t)
	mustExec(t, st, `INSERT INTO audit_log (action, created_at) VALUES ('x', ?)`, retAgo(500))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.PurgeAudit(ctx, 365, retNow); err == nil {
		t.Error("a cancelled context must abort the purge")
	}
}

func TestTrimRevisionsKeepsTheNewestPerProject(t *testing.T) {
	st, ctx := retStore(t)
	for p := 1; p <= 2; p++ {
		for rev := 1; rev <= 5+p; rev++ { // project 1: 6 revisions, project 2: 7
			mustExec(t, st, `INSERT INTO project_revisions (project_id, revision, created_at) VALUES (?, ?, ?)`, p, rev, retAgo(1))
		}
	}
	refs, err := st.TrimRevisions(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2+3 {
		t.Fatalf("trimmed %d revisions, want 5 (2 from project 1, 3 from project 2): %+v", len(refs), refs)
	}
	for _, ref := range refs {
		newestKept := map[int64]int{1: 3, 2: 4}[ref.ProjectID] // first kept revision number
		if ref.Revision >= newestKept {
			t.Errorf("trimmed %+v, but revisions >= %d must be kept", ref, newestKept)
		}
	}
	if got := count(t, st, "project_revisions"); got != 8 {
		t.Errorf("%d revisions left, want 4 per project = 8", got)
	}
	if refs, _ := st.TrimRevisions(ctx, 0); len(refs) != 0 {
		t.Error("0 means unlimited")
	}
}

func TestRetentionStats(t *testing.T) {
	st, ctx := retStore(t)
	mustExec(t, st, `INSERT INTO alert_events (id, created_at) VALUES (1, ?), (2, ?)`, retAgo(9), retAgo(3))
	mustExec(t, st, `INSERT INTO audit_log (action, created_at) VALUES ('x', ?)`, retAgo(2))
	got, err := st.RetentionStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AlertEvents.Rows != 2 || got.AlertEvents.Oldest != retAgo(9) {
		t.Errorf("alert events: %+v", got.AlertEvents)
	}
	if got.Audit.Rows != 1 || got.AlertDeliveries.Rows != 0 || got.AlertDeliveries.Oldest != "" {
		t.Errorf("audit=%+v deliveries=%+v", got.Audit, got.AlertDeliveries)
	}
	if got.DBBytes <= 0 {
		t.Errorf("db size = %d, want > 0", got.DBBytes)
	}
}

func TestRetentionRunRoundTrip(t *testing.T) {
	st, ctx := retStore(t)
	if r, err := st.LastRetentionRun(ctx); r != nil || err != nil {
		t.Fatalf("no run yet: %+v %v", r, err)
	}
	in := RetentionRun{At: retNow, Trigger: "manual", Audit: 4, AlertEvents: 2}
	if err := st.SaveRetentionRun(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ := st.LastRetentionRun(ctx)
	if got == nil || got.Audit != 4 || got.Trigger != "manual" || got.Total() != 6 {
		t.Errorf("round trip: %s", fmt.Sprintf("%+v", got))
	}
}
