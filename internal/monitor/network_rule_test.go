package monitor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Tests for the "network" rule type — drops/errors alerted on their INCREASE
// over a window, never their absolute value. Unlike evalResourceRules, this
// needs a real history store to query a window from, so it gets its own
// Monitor constructor rather than reusing newAlertMonitor (which wires a nil
// history.Store, fine for the resource-rule tests but not for these).

func newNetworkAlertMonitor(t *testing.T) (*Monitor, *store.Store, history.Store, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	hist := history.Open(ctx, history.Config{})
	t.Cleanup(func() { _ = hist.Close() })
	return New(st, nil, hist), st, hist, ctx
}

func addNetworkRule(t *testing.T, st *store.Store, ctx context.Context, metric string, threshold float64, windowSec int) int64 {
	t.Helper()
	id, err := st.CreateAlertRule(ctx, &store.AlertRule{
		Name: "Network " + metric, Enabled: true, Type: "network", Target: "",
		Config:   sprintf(`{"metric":"%s","threshold":%g,"windowSec":%d}`, metric, threshold, windowSec),
		Severity: "warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func netContainer() ContainerStat {
	return ContainerStat{HostID: 0, HostName: "local", ID: "c1", Name: "flaky-proxy", State: "running"}
}

// record seeds synthetic cumulative points for a container's metric at
// secondsAgo offsets from now, oldest-first.
func recordPoints(t *testing.T, hist history.Store, ctx context.Context, cid string, metric string, points map[int]float64) {
	t.Helper()
	for secondsAgo, v := range points {
		s := history.Sample{ContainerID: cid, HostID: 0, Time: time.Now().Add(-time.Duration(secondsAgo) * time.Second)}
		switch metric {
		case history.MetricNetDrops:
			s.NetDrops = v
		case history.MetricNetErrors:
			s.NetErrors = v
		}
		if err := hist.Record(ctx, []history.Sample{s}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNetworkRuleFiresOnWindowedIncrease: a drops counter that actually grew
// within the window must fire.
func TestNetworkRuleFiresOnWindowedIncrease(t *testing.T) {
	m, st, hist, ctx := newNetworkAlertMonitor(t)
	addNetworkRule(t, st, ctx, "netdrops", 5, 300)
	cs := netContainer()
	recordPoints(t, hist, ctx, cs.ID, history.MetricNetDrops, map[int]float64{250: 0, 10: 10})

	m.evalNetworkRules(ctx, snapOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("expected one fired event, got %d:\n%s", len(evs), dump(evs))
	}
	if evs[0].Kind != store.KindFiring {
		t.Errorf("kind = %q, want %q", evs[0].Kind, store.KindFiring)
	}
	if !strings.Contains(evs[0].Message, "dropped packets") || !strings.Contains(evs[0].Message, "10") {
		t.Errorf("message should say what increased and by how much: %q", evs[0].Message)
	}
}

// TestNetworkRuleStaysSilentOnHighButFlatCounter is the load-bearing case
// from NEXT.md's own framing: "a drops counter sitting at 12 since a bad
// afternoon last month is not an incident." A high absolute value with NO
// increase inside the window must never fire — that's the entire point of
// this rule type over a plain resource threshold.
func TestNetworkRuleStaysSilentOnHighButFlatCounter(t *testing.T) {
	m, st, hist, ctx := newNetworkAlertMonitor(t)
	addNetworkRule(t, st, ctx, "netdrops", 5, 300)
	cs := netContainer()
	// Sat at 500 for the whole window — no increase, however alarming the
	// absolute number looks.
	recordPoints(t, hist, ctx, cs.ID, history.MetricNetDrops, map[int]float64{250: 500, 10: 500})

	m.evalNetworkRules(ctx, snapOf(cs))

	if evs := events(t, st, ctx); len(evs) != 0 {
		t.Fatalf("a flat (non-increasing) counter must not fire, got %d:\n%s", len(evs), dump(evs))
	}
}

// TestNetworkRuleCounterResetDoesNotOverCountOrGoNegative: a container
// recreated mid-window resets the counter. The dip must not subtract from
// the total (which would either go negative or silently cancel a real
// increase), and must not throw the sum off.
func TestNetworkRuleCounterResetDoesNotOverCountOrGoNegative(t *testing.T) {
	m, st, hist, ctx := newNetworkAlertMonitor(t)
	addNetworkRule(t, st, ctx, "netdrops", 100, 300)
	cs := netContainer()
	// 0 -> 100 (+100), 100 -> 20 (reset, +0), 20 -> 50 (+30). True increase: 130.
	recordPoints(t, hist, ctx, cs.ID, history.MetricNetDrops, map[int]float64{280: 0, 200: 100, 100: 20, 10: 50})

	m.evalNetworkRules(ctx, snapOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("expected one fired event (130 >= 100 threshold), got %d:\n%s", len(evs), dump(evs))
	}
	if !strings.Contains(evs[0].Message, "130") {
		t.Errorf("message should report the reset-safe total (130), got %q", evs[0].Message)
	}
}

// TestNetworkRuleBelowThresholdStaysSilent: a real but small increase under
// the configured threshold must not fire.
func TestNetworkRuleBelowThresholdStaysSilent(t *testing.T) {
	m, st, hist, ctx := newNetworkAlertMonitor(t)
	addNetworkRule(t, st, ctx, "neterrors", 10, 300)
	cs := netContainer()
	recordPoints(t, hist, ctx, cs.ID, history.MetricNetErrors, map[int]float64{250: 0, 10: 3})

	m.evalNetworkRules(ctx, snapOf(cs))

	if evs := events(t, st, ctx); len(evs) != 0 {
		t.Fatalf("an increase (3) below the threshold (10) must not fire, got %d:\n%s", len(evs), dump(evs))
	}
}

// TestNetworkRuleUsesCooldownNotStateLifecycle: unlike evalResourceRules, a
// still-true network condition does not escalate/resolve — it just respects
// the plain fire() cooldown, same as restart/state rules.
func TestNetworkRuleUsesCooldownNotStateLifecycle(t *testing.T) {
	m, st, hist, ctx := newNetworkAlertMonitor(t)
	addNetworkRule(t, st, ctx, "netdrops", 5, 300)
	cs := netContainer()
	recordPoints(t, hist, ctx, cs.ID, history.MetricNetDrops, map[int]float64{250: 0, 10: 10})

	m.evalNetworkRules(ctx, snapOf(cs))
	m.evalNetworkRules(ctx, snapOf(cs)) // same window, same data — still under cooldown

	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("a second evaluation inside the cooldown must not re-fire, got %d:\n%s", len(evs), dump(evs))
	}
}

// TestNetworkThroughputRuleUsesLiveRate: netrx_rate/nettx_rate resource
// rules read the same live per-poll rate the dashboard shows — a plain
// absolute-threshold rule, no history query involved.
func TestNetworkThroughputRuleUsesLiveRate(t *testing.T) {
	cs := ContainerStat{NetRxRate: 12_000_000, NetTxRate: 500}
	rx, ok := cs.metric("netrx_rate")
	if !ok || rx != 12_000_000 {
		t.Errorf("netrx_rate = %v (ok=%v), want 12000000", rx, ok)
	}
	tx, ok := cs.metric("nettx_rate")
	if !ok || tx != 500 {
		t.Errorf("nettx_rate = %v (ok=%v), want 500", tx, ok)
	}
}

func TestNetworkThroughputRuleFiresAboveThreshold(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id, err := st.CreateAlertRule(ctx, &store.AlertRule{
		Name: "RX spike", Enabled: true, Type: "resource", Target: "",
		Config: `{"metric":"netrx_rate","op":">","threshold":10000000,"durationSec":30}`, Severity: "warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := netContainer()
	cs.NetRxRate = 20_000_000 // 20 MB/s, above the 10 MB/s threshold

	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("expected the throughput rule to fire, got %d:\n%s", len(evs), dump(evs))
	}
	if !strings.Contains(evs[0].Message, "RX") || !strings.Contains(evs[0].Message, "/s") {
		t.Errorf("message should state direction and units: %q", evs[0].Message)
	}

	// And below threshold, it must resolve rather than stay firing.
	calm := cs
	calm.NetRxRate = 1_000_000
	m.ready(id, calm.ID)
	m.evalResourceRules(ctx, snapOf(calm), hostsOf(calm))
	evs = events(t, st, ctx)
	if len(evs) != 2 || evs[0].Kind != store.KindResolved {
		t.Fatalf("expected firing + resolved, got %d:\n%s", len(evs), dump(evs))
	}
}
