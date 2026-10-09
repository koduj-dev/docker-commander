package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/monitor"
)

// rankTopTalkers is handleTopTalkers' testable core (ranking/skip logic),
// split out so it can be exercised against a real in-memory history.Store
// without a live Monitor/Docker daemon behind it. See history_handler.go.

func seedRate(t *testing.T, hist history.Store, ctx context.Context, cid, metric string, secondsAgoToValue map[int]float64) {
	t.Helper()
	for secondsAgo, v := range secondsAgoToValue {
		s := history.Sample{ContainerID: cid, Time: time.Now().Add(-time.Duration(secondsAgo) * time.Second)}
		switch metric {
		case history.MetricNetRx:
			s.NetRx = v
		case history.MetricNetTx:
			s.NetTx = v
		}
		if err := hist.Record(ctx, []history.Sample{s}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRankTopTalkersOrdersBySelectedMetric: the whole point of the endpoint —
// containers must come back ranked by the requested metric's windowed rate,
// most active first.
func TestRankTopTalkersOrdersBySelectedMetric(t *testing.T) {
	ctx := context.Background()
	hist := history.Open(ctx, history.Config{})
	t.Cleanup(func() { hist.Close() })

	// c1: 100 bytes/s RX. c2: 500 bytes/s RX (busier). c3: no data at all.
	seedRate(t, hist, ctx, "c1", history.MetricNetRx, map[int]float64{10: 0, 0: 1000})
	seedRate(t, hist, ctx, "c2", history.MetricNetRx, map[int]float64{10: 0, 0: 5000})
	meta := map[string]topTalkerMeta{
		"c1": {name: "quiet", hostID: 0}, "c2": {name: "busy", hostID: 0}, "c3": {name: "silent", hostID: 0},
	}

	out, _, err := rankTopTalkers(ctx, hist, []string{"c1", "c2", "c3"}, meta, time.Now().Add(-time.Minute), history.MetricNetRx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("c3 has no history and must be absent, got %d rows: %+v", len(out), out)
	}
	if out[0].Name != "busy" || out[1].Name != "quiet" {
		t.Fatalf("expected busy first (descending rate), got %+v", out)
	}
	if out[0].Rate <= out[1].Rate {
		t.Errorf("rows must be sorted descending by rate: %+v", out)
	}
}

// TestRankTopTalkersTotalSumsBothDirections: metric "total" (the dashboard
// widget's default) must be rx+tx, not just one direction.
func TestRankTopTalkersTotalSumsBothDirections(t *testing.T) {
	ctx := context.Background()
	hist := history.Open(ctx, history.Config{})
	t.Cleanup(func() { hist.Close() })

	seedRate(t, hist, ctx, "c1", history.MetricNetRx, map[int]float64{10: 0, 0: 1000})
	seedRate(t, hist, ctx, "c1", history.MetricNetTx, map[int]float64{10: 0, 0: 2000})
	meta := map[string]topTalkerMeta{"c1": {name: "c1"}}

	out, _, err := rankTopTalkers(ctx, hist, []string{"c1"}, meta, time.Now().Add(-time.Minute), "total", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected one row, got %d", len(out))
	}
	if out[0].Rate != out[0].RxRate+out[0].TxRate {
		t.Errorf("total metric should be rx+tx: rate=%v rx=%v tx=%v", out[0].Rate, out[0].RxRate, out[0].TxRate)
	}
}

// TestRankTopTalkersRespectsLimit: the caller's limit must actually cap the
// result, not just influence it.
func TestRankTopTalkersRespectsLimit(t *testing.T) {
	ctx := context.Background()
	hist := history.Open(ctx, history.Config{})
	t.Cleanup(func() { hist.Close() })

	ids := []string{"a", "b", "c", "d"}
	meta := map[string]topTalkerMeta{}
	for i, id := range ids {
		seedRate(t, hist, ctx, id, history.MetricNetRx, map[int]float64{10: 0, 0: float64((i + 1) * 100)})
		meta[id] = topTalkerMeta{name: id}
	}

	out, total, err := rankTopTalkers(ctx, hist, ids, meta, time.Now().Add(-time.Minute), history.MetricNetRx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("limit=2 should return exactly 2 rows, got %d", len(out))
	}
	// The two busiest ("d" and "c") must be the ones kept, not an arbitrary prefix.
	if out[0].Name != "d" || out[1].Name != "c" {
		t.Errorf("limit should keep the busiest rows, got %+v", out)
	}
	// total must reflect every ranked container, not just the ones that fit
	// under limit — this is what lets a caller show "2 of 4" instead of
	// silently rendering the truncated slice as if it were everything.
	if total != 4 {
		t.Errorf("total should count all 4 ranked containers regardless of limit, got %d", total)
	}
}

// TestTopTalkersNameFilterAppliesBeforeTheLimit is the key contract of the
// name filter: with more ranked containers than the limit, a match that ranks
// below the cut must still be found, because filtering happens before
// ranking — not on the already-truncated page.
func TestTopTalkersNameFilterAppliesBeforeTheLimit(t *testing.T) {
	ctx := context.Background()
	hist := history.Open(ctx, history.Config{})
	t.Cleanup(func() { hist.Close() })

	var snap []monitor.ContainerStat
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("c%02d", i)
		// Busier as i grows; "needle" (i == 0) is the quietest of all 61.
		seedRate(t, hist, ctx, id, history.MetricNetRx, map[int]float64{10: 0, 0: float64((i + 1) * 1000)})
		snap = append(snap, monitor.ContainerStat{ID: id, Name: "svc-" + id, State: "running", HostID: 0})
	}
	seedRate(t, hist, ctx, "needle", history.MetricNetRx, map[int]float64{10: 0, 0: 10})
	snap = append(snap,
		monitor.ContainerStat{ID: "needle", Name: "My-Needle-Cache", State: "running", HostID: 0},
		monitor.ContainerStat{ID: "stopped", Name: "needle-old", State: "exited", HostID: 0},
		monitor.ContainerStat{ID: "other", Name: "needle-remote", State: "running", HostID: 7},
	)
	since := time.Now().Add(-time.Minute)

	// Unfiltered: the limit cuts the quietest ones — needle is not in the page,
	// but total still says there are more.
	ids, meta := topTalkerCandidates(snap, 0, "")
	out, total, err := rankTopTalkers(ctx, hist, ids, meta, since, history.MetricNetRx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 50 || total != 61 {
		t.Fatalf("unfiltered: got %d rows / total %d, want 50 / 61", len(out), total)
	}
	for _, r := range out {
		if r.ID == "needle" {
			t.Fatal("needle should be below the cut without a filter")
		}
	}

	// Filtered (case-insensitively): needle is found, and only it — not the
	// stopped container, nor the one on another host.
	ids, meta = topTalkerCandidates(snap, 0, "needle")
	out, total, err = rankTopTalkers(ctx, hist, ids, meta, since, history.MetricNetRx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "needle" || total != 1 {
		t.Fatalf("filtered: got %+v total %d, want just needle", out, total)
	}
}

// --- HTTP-level validation ---------------------------------------------------

func TestTopTalkersHandlerRejectsBadQueryParams(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})

	code, body := a.do("GET", "/api/stats/top-talkers", nil)
	if code != 200 {
		t.Errorf("default params should succeed (even with an empty result), got %d", code)
	}
	// The response must always carry a total, even when it's 0 — the
	// frontend's "N of TOTAL" indicator depends on the field being present,
	// not just non-empty containers.
	if _, ok := body["total"]; !ok {
		t.Errorf("response should carry a 'total' field, got %+v", body)
	}
	if code, _ := a.do("GET", "/api/stats/top-talkers?window=3d", nil); code != 400 {
		t.Errorf("an unlisted window should 400, got %d", code)
	}
	if code, _ := a.do("GET", "/api/stats/top-talkers?metric=bogus", nil); code != 400 {
		t.Errorf("an unlisted metric should 400, got %d", code)
	}
	// q is a free-text filter, not a whitelisted value — any string, including
	// one matching nothing, must still succeed rather than 400.
	if code, _ := a.do("GET", "/api/stats/top-talkers?q=nginx", nil); code != 200 {
		t.Errorf("a name filter with no matches should still succeed with an empty result, got %d", code)
	}
}
