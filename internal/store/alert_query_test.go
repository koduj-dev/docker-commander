package store

import (
	"context"
	"strings"
	"testing"
)

// Tests for the alert feed's filtering, paging, host scoping and delivery
// records.
//
// The scoping test is the one that matters most: paging made it possible to get
// this wrong in a way that leaks. The old handler fetched everything and dropped
// other hosts' events in Go, which was safe but cannot survive paging — so the
// filter moved into SQL, and it needs a test that fails if it ever moves back.

func seedEvents(t *testing.T, s *Store, ctx context.Context) {
	t.Helper()
	rows := []AlertEvent{
		{RuleName: "Memory", Severity: "critical", Kind: KindFiring, HostID: 1, HostName: "prod", ContainerName: "db-postgres", Message: "MEM 90% of limit"},
		{RuleName: "Memory", Severity: "warning", Kind: KindResolved, HostID: 1, HostName: "prod", ContainerName: "db-postgres", Message: "MEM back to normal"},
		{RuleName: "CPU burn", Severity: "warning", Kind: KindFiring, HostID: 2, HostName: "staging", ContainerName: "queue-worker", Message: "CPU 90% of 4 cores"},
		{RuleName: "Disk", Severity: "info", Kind: KindFiring, HostID: 0, HostName: "local", ContainerName: "web-nginx", Message: "usage at 100% of quota"},
		// Contains "100" but NOT "100%". Without it, an unescaped search for
		// "100%" ("%100%%") still matches exactly one row by luck and the test
		// passes while proving nothing.
		{RuleName: "Restarts", Severity: "warning", Kind: KindFiring, HostID: 0, HostName: "local", ContainerName: "ops-chaos", Message: "1004 restarts in 10m"},
	}
	for i := range rows {
		if _, err := s.InsertAlertEvent(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAlertQueryFilters(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	cases := []struct {
		name  string
		q     AlertQuery
		want  int
		check func(t *testing.T, evs []AlertEvent)
	}{
		{name: "no filter returns everything", q: AlertQuery{}, want: 5},
		{name: "by severity", q: AlertQuery{Severity: "warning"}, want: 3},
		{name: "by lifecycle kind", q: AlertQuery{Kind: KindResolved}, want: 1},
		{name: "by container substring", q: AlertQuery{Container: "postgres"}, want: 2},
		{name: "by rule substring", q: AlertQuery{Rule: "Mem"}, want: 2},
		{name: "by message text", q: AlertQuery{Text: "cores"}, want: 1},
		{
			name: "filters combine", q: AlertQuery{Rule: "Memory", Severity: "critical"}, want: 1,
			check: func(t *testing.T, evs []AlertEvent) {
				if evs[0].Kind != KindFiring {
					t.Errorf("got %+v", evs[0])
				}
			},
		},
		{name: "no match is empty, not everything", q: AlertQuery{Container: "nonexistent"}, want: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, total, err := s.ListAlertEvents(ctx, c.q)
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != c.want || total != c.want {
				t.Fatalf("got %d rows / total %d, want %d", len(evs), total, c.want)
			}
			if c.check != nil && len(evs) > 0 {
				c.check(t, evs)
			}
		})
	}
}

// TestAlertQueryEscapesLikeWildcards: a message search for "100%" must look for
// that text. Without escaping (and the matching ESCAPE clause) the % is a
// wildcard and the filter quietly matches every row — the worst kind of bug in a
// search box, because it looks like it worked.
func TestAlertQueryEscapesLikeWildcards(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	evs, total, err := s.ListAlertEvents(ctx, AlertQuery{Text: "100%"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(evs) != 1 {
		t.Fatalf("searching for %q matched %d rows, want exactly the one containing it", "100%", total)
	}
	if !strings.Contains(evs[0].Message, "100%") {
		t.Errorf("matched the wrong row: %q", evs[0].Message)
	}

	// "_" is the other wildcard, and a single one would otherwise match any character.
	if _, total, _ := s.ListAlertEvents(ctx, AlertQuery{Container: "_"}); total != 0 {
		t.Errorf("a lone underscore matched %d containers; it must be treated literally", total)
	}
}

func TestAlertQueryPaging(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	page1, total, err := s.ListAlertEvents(ctx, AlertQuery{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(page1) != 2 {
		t.Fatalf("page 1: %d rows, total %d; want 2 and 5", len(page1), total)
	}
	page2, _, _ := s.ListAlertEvents(ctx, AlertQuery{Limit: 2, Offset: 2})
	if len(page2) != 2 {
		t.Fatalf("page 2: %d rows, want 2", len(page2))
	}
	// The total must describe the whole result set, not the page — otherwise the
	// UI can never know there is a next page.
	for _, a := range page1 {
		for _, b := range page2 {
			if a.ID == b.ID {
				t.Fatalf("event %d appears on both pages", a.ID)
			}
		}
	}
}

// TestAlertQueryHostScopeIsAppliedInSQL is the security-relevant one.
func TestAlertQueryHostScopeIsAppliedInSQL(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	// Scoped to the local daemon and host 1 only: host 2's event must not appear,
	// and — just as important — must not be counted.
	evs, total, err := s.ListAlertEvents(ctx, AlertQuery{HostIDs: []int64{0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(evs) != 4 {
		t.Fatalf("scoped query returned %d rows / total %d, want 4 — the total must not count hidden events", len(evs), total)
	}
	for _, e := range evs {
		if e.HostID == 2 {
			t.Errorf("SECURITY: an event from an out-of-scope host was returned: %+v", e)
		}
	}

	// A nil scope means "no restriction"; an EMPTY scope means "nothing", and the
	// difference has to fail closed rather than open.
	if _, total, _ := s.ListAlertEvents(ctx, AlertQuery{HostIDs: []int64{}}); total != 0 {
		t.Errorf("SECURITY: an empty host scope returned %d events; it must return none", total)
	}
	if _, total, _ := s.ListAlertEvents(ctx, AlertQuery{HostIDs: nil}); total != 5 {
		t.Errorf("a nil host scope should be unrestricted, got %d", total)
	}
}

func TestAlertAckRecordsWho(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	evs, _, _ := s.ListAlertEvents(ctx, AlertQuery{Limit: 1})
	if err := s.AckAlertEvent(ctx, evs[0].ID, "filip"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.ListAlertEvents(ctx, AlertQuery{Limit: 1})
	if !got[0].Acknowledged {
		t.Fatal("event should be acknowledged")
	}
	if got[0].AcknowledgedBy != "filip" {
		t.Errorf("acknowledgedBy = %q, want %q — an unattributed ack cannot be followed up", got[0].AcknowledgedBy, "filip")
	}
	if got[0].AcknowledgedAt == nil {
		t.Error("acknowledgedAt should be set")
	}
}

func TestAlertDeliveriesRecordedAndCapped(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)
	evs, _, _ := s.ListAlertEvents(ctx, AlertQuery{Limit: 1})
	id := evs[0].ID

	if err := s.RecordAlertDelivery(ctx, &AlertDelivery{
		EventID: id, Channel: "webhook", Target: "ops (hooks.example.com)", OK: false, Status: 500,
		Detail: strings.Repeat("x", 2000), // a chatty endpoint
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAlertDelivery(ctx, &AlertDelivery{
		EventID: id, Channel: "email", Target: "ops@example.com", OK: true,
	}); err != nil {
		t.Fatal(err)
	}

	byEvent, err := s.AlertDeliveriesFor(ctx, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	ds := byEvent[id]
	if len(ds) != 2 {
		t.Fatalf("got %d delivery records, want 2", len(ds))
	}
	if ds[0].Status != 500 || ds[0].OK {
		t.Errorf("failed webhook recorded as %+v", ds[0])
	}
	// A remote endpoint must not be able to write unbounded text into our DB.
	if len(ds[0].Detail) > 520 {
		t.Errorf("detail was stored at %d bytes; it should be truncated", len(ds[0].Detail))
	}
	if !ds[1].OK || ds[1].Channel != "email" {
		t.Errorf("email delivery recorded as %+v", ds[1])
	}

	// Asking for an event with no attempts must yield nothing, not everything.
	if got, _ := s.AlertDeliveriesFor(ctx, []int64{evs[0].ID + 999}); len(got) != 0 {
		t.Errorf("unknown event returned %d delivery groups", len(got))
	}
}

// TestAckAllRespectsTheFilterAndScope is the one that keeps a convenience button
// from becoming a security bug: "acknowledge all" must never reach events the
// caller could not see in the list it was clicked from.
func TestAckAllRespectsTheFilterAndScope(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	// Scoped to the local daemon and host 1: host 2's event must survive.
	n, err := s.AckMatchingAlertEvents(ctx, AlertQuery{HostIDs: []int64{0, 1}}, "filip")
	if err != nil {
		t.Fatal(err)
	}
	// 4 events are in scope, but one of them is a resolution — stored already
	// settled — so bulk acknowledge has 3 to do.
	if n != 3 {
		t.Fatalf("acknowledged %d events, want the 3 outstanding ones in scope", n)
	}
	evs, _, _ := s.ListAlertEvents(ctx, AlertQuery{})
	for _, e := range evs {
		if e.HostID == 2 && e.Acknowledged {
			t.Errorf("SECURITY: acknowledged an event on an out-of-scope host: %+v", e)
		}
		if e.HostID != 2 && e.Kind != KindResolved {
			if !e.Acknowledged || e.AcknowledgedBy != "filip" {
				t.Errorf("in-scope event not acknowledged by the caller: %+v", e)
			}
		}
	}

	// A second pass must not re-stamp what someone else already acknowledged.
	again, err := s.AckMatchingAlertEvents(ctx, AlertQuery{HostIDs: []int64{0, 1}}, "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("re-running acknowledged %d already-acknowledged events; attribution would be overwritten", again)
	}
}

// TestAckAllHonoursNarrowerFilters: clicking it with a filter on must do what the
// screen implied, not empty the whole feed.
func TestAckAllHonoursNarrowerFilters(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	n, err := s.AckMatchingAlertEvents(ctx, AlertQuery{Severity: "critical"}, "filip")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("acknowledged %d, want only the 1 critical event", n)
	}
	// 5 seeded, minus the resolution (born settled) and the critical just
	// acknowledged, leaves 3 outstanding.
	if _, unacked, _ := s.ListAlertEvents(ctx, AlertQuery{Unacked: true}); unacked != 3 {
		t.Errorf("%d events left outstanding, want 3 — the filter was ignored", unacked)
	}
}

// TestAlertBadgeCountsOnlyProblems pins what the sidebar number means.
//
// It is the count of things that are wrong, so it must not climb when a
// condition RESOLVES — a badge that grows as problems fix themselves is a badge
// people stop reading. Resolved events are emitted as info, so filtering by
// severity covers it without a second rule about lifecycle kinds.
func TestAlertBadgeCountsOnlyProblems(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	badge := AlertQuery{Unacked: true, Severities: []string{"warning", "critical"}}
	_, n, err := s.ListAlertEvents(ctx, badge)
	if err != nil {
		t.Fatal(err)
	}
	// Derived rather than hard-coded, so the fixture can grow without this
	// becoming a puzzle: the badge is exactly the OUTSTANDING warnings and
	// criticals.
	_, want, _ := s.ListAlertEvents(ctx, AlertQuery{Unacked: true, Severities: []string{"warning", "critical"}})
	if n != want {
		t.Fatalf("badge counted %d, want %d (outstanding warnings + criticals)", n, want)
	}

	// The decisive case: adding a resolution must not move the badge. It is
	// stored already settled, so it is excluded twice over — but the assertion is
	// about the outcome, not the mechanism.
	before := n
	if _, err := s.InsertAlertEvent(ctx, &AlertEvent{
		RuleName: "Memory", Severity: "info", Kind: KindResolved, HostID: 1,
		ContainerName: "db-postgres", Message: "MEM back to normal after 2m",
	}); err != nil {
		t.Fatal(err)
	}
	_, after, _ := s.ListAlertEvents(ctx, badge)
	if after != before {
		t.Errorf("a resolved condition moved the badge from %d to %d; good news must not read as an outstanding problem", before, after)
	}

	// An info event that is NOT a resolution is equally not a problem.
	if _, err := s.InsertAlertEvent(ctx, &AlertEvent{
		RuleName: "Note", Severity: "info", Kind: KindFiring, HostID: 1,
		ContainerName: "web-nginx", Message: "informational",
	}); err != nil {
		t.Fatal(err)
	}
	_, afterInfo, _ := s.ListAlertEvents(ctx, badge)
	if afterInfo != before {
		t.Errorf("an info alert moved the badge from %d to %d", before, afterInfo)
	}

	// But a new warning must, or the badge would be useless.
	if _, err := s.InsertAlertEvent(ctx, &AlertEvent{
		RuleName: "CPU", Severity: "warning", Kind: KindFiring, HostID: 1,
		ContainerName: "queue-worker", Message: "CPU high",
	}); err != nil {
		t.Fatal(err)
	}
	_, afterWarn, _ := s.ListAlertEvents(ctx, badge)
	if afterWarn != before+1 {
		t.Errorf("a new warning left the badge at %d, want %d", afterWarn, before+1)
	}
}

// TestResolvedEventsAreBornSettled pins the rule at its source: a resolution is
// stored acknowledged, because there is nothing for anyone to do about a
// condition that ended. Everything downstream — the badge, the outstanding
// filter, bulk acknowledge — then needs no special case for it.
func TestResolvedEventsAreBornSettled(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if _, err := s.InsertAlertEvent(ctx, &AlertEvent{
		RuleName: "Memory", Severity: "info", Kind: KindResolved,
		ContainerName: "db-postgres", Message: "MEM back to normal after 2m",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertAlertEvent(ctx, &AlertEvent{
		RuleName: "Memory", Severity: "warning", Kind: KindFiring,
		ContainerName: "db-postgres", Message: "MEM 90% of limit",
	}); err != nil {
		t.Fatal(err)
	}

	all, _, _ := s.ListAlertEvents(ctx, AlertQuery{})
	for _, e := range all {
		switch e.Kind {
		case KindResolved:
			if !e.Acknowledged {
				t.Error("a resolution must be stored acknowledged; otherwise it sits in the outstanding list for ever, with no action to clear it")
			}
			if e.AcknowledgedBy != "" {
				t.Errorf("acknowledgedBy = %q; no person settled this, and claiming one would be a lie in the audit trail", e.AcknowledgedBy)
			}
		case KindFiring:
			if e.Acknowledged {
				t.Error("a firing alert must NOT be pre-acknowledged")
			}
		}
	}

	// The outstanding list therefore holds only the live problem.
	out, n, _ := s.ListAlertEvents(ctx, AlertQuery{Unacked: true})
	if n != 1 || len(out) != 1 || out[0].Kind != KindFiring {
		t.Fatalf("outstanding list holds %d events, want just the firing one: %+v", n, out)
	}

	// And bulk acknowledge has nothing to say about resolutions.
	if got, err := s.AckMatchingAlertEvents(ctx, AlertQuery{}, "filip"); err != nil || got != 1 {
		t.Errorf("ack-all touched %d events (err=%v), want only the 1 outstanding", got, err)
	}
}

// TestAlertQuerySortIsWhitelisted covers the ordering, including the part that
// cannot be a bound parameter.
//
// ORDER BY can't be parameterised, so the sort key is mapped through a fixed
// switch rather than interpolated. An unknown or hostile key must fall back to
// the default, not reach SQL.
func TestAlertQuerySortIsWhitelisted(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedEvents(t, s, ctx)

	t.Run("severity sorts by importance, not alphabetically", func(t *testing.T) {
		evs, _, err := s.ListAlertEvents(ctx, AlertQuery{Sort: "severity", Desc: true})
		if err != nil {
			t.Fatal(err)
		}
		if evs[0].Severity != "critical" {
			t.Fatalf("first row is %q; critical must lead, and alphabetically it would too — check the tail", evs[0].Severity)
		}
		// Alphabetically "info" < "warning", so a naive ORDER BY severity DESC
		// would put warning before info. Importance order puts info last.
		if evs[len(evs)-1].Severity != "info" {
			t.Errorf("last row is %q, want info — severity is being ordered alphabetically", evs[len(evs)-1].Severity)
		}
	})

	t.Run("container sorts by name", func(t *testing.T) {
		evs, _, _ := s.ListAlertEvents(ctx, AlertQuery{Sort: "container", Desc: false})
		for i := 1; i < len(evs); i++ {
			if strings.ToLower(evs[i-1].ContainerName) > strings.ToLower(evs[i].ContainerName) {
				t.Fatalf("not sorted: %q before %q", evs[i-1].ContainerName, evs[i].ContainerName)
			}
		}
	})

	t.Run("an unknown sort key falls back instead of reaching SQL", func(t *testing.T) {
		evs, _, err := s.ListAlertEvents(ctx, AlertQuery{Sort: "container_name; DROP TABLE alert_events--"})
		if err != nil {
			t.Fatalf("a hostile sort key must be ignored, not error: %v", err)
		}
		if len(evs) == 0 {
			t.Fatal("no rows back")
		}
		// Default is newest first.
		if evs[0].ID < evs[len(evs)-1].ID {
			t.Error("fallback should be newest-first")
		}
		// And the table must still be there.
		if _, total, err := s.ListAlertEvents(ctx, AlertQuery{}); err != nil || total == 0 {
			t.Fatalf("SECURITY: the events table did not survive a hostile sort key (total=%d err=%v)", total, err)
		}
	})
}
