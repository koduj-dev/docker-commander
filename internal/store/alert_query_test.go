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
