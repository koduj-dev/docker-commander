package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/store"
)

var errNetworkRulesTestBoom = errors.New("boom: simulated history backend failure")

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

// TestRecordHistorySkipsAFailedSample is the end-to-end regression for the
// P1 this PR's own review caught: a container whose SampleStats call failed
// this poll must never be recorded into history as an all-zero reading — the
// network rule type and Top Talkers both derive an INCREASE from consecutive
// history points, and a real value dipping to zero for one point (then
// recovering) would otherwise look exactly like the traffic/drops the dip
// itself was hiding.
func TestRecordHistorySkipsAFailedSample(t *testing.T) {
	m, _, hist, ctx := newNetworkAlertMonitor(t)
	cs := netContainer()
	cs.Sampled = true
	cs.NetDrops = 500

	m.recordHistory(ctx, snapOf(cs))

	failed := cs
	failed.Sampled = false
	failed.NetDrops = 0 // what a failed SampleStats call actually leaves behind
	m.recordHistory(ctx, snapOf(failed))

	pts, err := hist.Query(ctx, cs.ID, history.MetricNetDrops, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("expected exactly the one real sample recorded, the failed poll must be skipped, got %d points: %+v", len(pts), pts)
	}
	if pts[0].V != 500 {
		t.Errorf("the recorded point should be the real value, got %v", pts[0].V)
	}
}

// queryCountingStore wraps a real history.Store and counts calls, so a test
// can assert evalNetworkRules batches its reads instead of issuing one Query
// per (rule, container) pair. failQueryAll, when set, makes every QueryAll
// call fail instead of reaching the real store — for the "don't retry a
// failed batch within the same poll" regression below.
type queryCountingStore struct {
	history.Store
	queryCalls    int
	queryAllCalls int
	failQueryAll  error
}

func (s *queryCountingStore) Query(ctx context.Context, containerID, metric string, since time.Time) ([]history.Point, error) {
	s.queryCalls++
	return s.Store.Query(ctx, containerID, metric, since)
}

func (s *queryCountingStore) QueryAll(ctx context.Context, metric string, since time.Time, containerIDs []string) (map[string][]history.Point, error) {
	s.queryAllCalls++
	if s.failQueryAll != nil {
		return nil, s.failQueryAll
	}
	return s.Store.QueryAll(ctx, metric, since, containerIDs)
}

// TestNetworkRulesBatchHistoryReads is the regression for the P2 this PR's
// own review caught: with N containers and M network rules sharing one
// (metric, window), the naive shape issued N×M sequential Query calls every
// 15s poll — with Redis history that is thousands of round trips on a busy
// host, all on the goroutine that has to finish before the next stats poll
// can run. It must instead be a small, container-count-independent number of
// batched QueryAll calls, and Query itself must never be used here at all.
func TestNetworkRulesBatchHistoryReads(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	spy := &queryCountingStore{Store: history.Open(ctx, history.Config{})}
	t.Cleanup(func() { _ = spy.Close() })
	m := New(st, nil, spy)

	// Two rules sharing the same (metric, window) — must still be ONE
	// QueryAll call between them, not one per rule.
	addNetworkRule(t, st, ctx, "netdrops", 5, 300)
	addNetworkRule(t, st, ctx, "netdrops", 50, 300)

	const containers = 25
	snap := make(map[string]ContainerStat, containers)
	for i := 0; i < containers; i++ {
		id := sprintf("c%d", i)
		snap[id] = ContainerStat{HostID: 0, HostName: "local", ID: id, Name: id, State: "running"}
		spy.Record(ctx, []history.Sample{
			{ContainerID: id, Time: time.Now().Add(-250 * time.Second), NetDrops: 0},
			{ContainerID: id, Time: time.Now().Add(-10 * time.Second), NetDrops: 10},
		})
	}

	m.evalNetworkRules(ctx, snap)

	if spy.queryCalls != 0 {
		t.Errorf("evalNetworkRules must never call the per-container Query, got %d calls", spy.queryCalls)
	}
	if spy.queryAllCalls != 1 {
		t.Errorf("two rules sharing one (metric, window) should batch into 1 QueryAll call, got %d (with %d containers, a per-pair loop would have made %d Query calls)",
			spy.queryAllCalls, containers, containers*2)
	}
}

// TestNetworkRulesDoNotRetryAFailedBatchWithinOnePoll is the regression for
// the P2 the second review pass caught: a failed QueryAll wasn't cached, so
// rules sharing that (metric, window) each re-triggered the same failing
// call — during a Redis outage, N rules sharing one window made N sequential
// failing round trips instead of one, on the same goroutine that has to
// finish before the next poll can run.
func TestNetworkRulesDoNotRetryAFailedBatchWithinOnePoll(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	spy := &queryCountingStore{Store: history.Open(ctx, history.Config{}), failQueryAll: errNetworkRulesTestBoom}
	t.Cleanup(func() { _ = spy.Close() })
	m := New(st, nil, spy)

	// Three rules sharing one (metric, window) — the failing query must
	// still be attempted only once for the whole poll.
	addNetworkRule(t, st, ctx, "netdrops", 5, 300)
	addNetworkRule(t, st, ctx, "netdrops", 50, 300)
	addNetworkRule(t, st, ctx, "netdrops", 500, 300)
	cs := netContainer()

	m.evalNetworkRules(ctx, snapOf(cs))

	if spy.queryAllCalls != 1 {
		t.Errorf("a failing batch shared by 3 rules should still be attempted once per poll, got %d calls", spy.queryAllCalls)
	}
}

// TestNetworkThroughputRuleUsesLiveRate: netrx_rate/nettx_rate resource
// rules read the same live per-poll rate the dashboard shows — a plain
// absolute-threshold rule, no history query involved.
// TestNetworkThroughputRuleUsesLiveRate: netrx_rate/nettx_rate is unavailable
// (ok=false) whenever applyNetRates hasn't actually computed it this poll —
// a plain zero-valued ContainerStat (as a failed sample, a first poll, or a
// counter reset all produce) must read as "unmeasured", never as "measured,
// genuinely zero throughput".
func TestNetworkThroughputRuleUsesLiveRate(t *testing.T) {
	cs := ContainerStat{NetRxRate: 12_000_000, NetRxRateOK: true, NetTxRate: 500, NetTxRateOK: true}
	rx, ok := cs.metric("netrx_rate")
	if !ok || rx != 12_000_000 {
		t.Errorf("netrx_rate = %v (ok=%v), want 12000000, true", rx, ok)
	}
	tx, ok := cs.metric("nettx_rate")
	if !ok || tx != 500 {
		t.Errorf("nettx_rate = %v (ok=%v), want 500, true", tx, ok)
	}

	unmeasured := ContainerStat{} // no rate ever computed this poll
	if _, ok := unmeasured.metric("netrx_rate"); ok {
		t.Error("a rate that was never computed must read as unavailable (ok=false), not as a real zero")
	}
	if _, ok := unmeasured.metric("nettx_rate"); ok {
		t.Error("a rate that was never computed must read as unavailable (ok=false), not as a real zero")
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
	cs.NetRxRate, cs.NetRxRateOK = 20_000_000, true // 20 MB/s, above the 10 MB/s threshold

	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("expected the throughput rule to fire, got %d:\n%s", len(evs), dump(evs))
	}
	if !strings.Contains(evs[0].Message, "RX") || !strings.Contains(evs[0].Message, "/s") {
		t.Errorf("message should state direction and units: %q", evs[0].Message)
	}

	// And below threshold (genuinely MEASURED, not just unavailable), it must
	// resolve rather than stay firing.
	calm := cs
	calm.NetRxRate = 1_000_000
	m.ready(id, calm.ID)
	m.evalResourceRules(ctx, snapOf(calm), hostsOf(calm))
	evs = events(t, st, ctx)
	if len(evs) != 2 || evs[0].Kind != store.KindResolved {
		t.Fatalf("expected firing + resolved, got %d:\n%s", len(evs), dump(evs))
	}
}

// TestNetworkThroughputConditionSurvivesAnUnmeasuredPoll is the regression
// for the P1 the second review pass caught: netrx_rate/nettx_rate used to
// return (0, true) whenever no rate was available (a failed sample, or the
// poll right after one), which the resolve sweep couldn't tell apart from
// "measured, genuinely back under threshold" — so a transient stats hiccup
// on an already-firing throughput condition announced a false recovery and
// split one ongoing incident into a resolve/fire pair.
func TestNetworkThroughputConditionSurvivesAnUnmeasuredPoll(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id, err := st.CreateAlertRule(ctx, &store.AlertRule{
		Name: "RX spike", Enabled: true, Type: "resource", Target: "",
		Config: `{"metric":"netrx_rate","op":">","threshold":10000000,"durationSec":30}`, Severity: "warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := netContainer()
	cs.NetRxRate, cs.NetRxRateOK = 20_000_000, true

	// Poll 1: fires.
	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))
	if evs := events(t, st, ctx); len(evs) != 1 || evs[0].Kind != store.KindFiring {
		t.Fatalf("expected exactly one firing event, got %d:\n%s", len(events(t, st, ctx)), dump(evs))
	}

	// Poll 2: the sample failed — NetRxRateOK false, exactly what a real
	// failed SampleStats call (or the poll right after one) leaves behind.
	// Real throughput never actually dropped; we just couldn't measure it.
	unmeasured := cs
	unmeasured.NetRxRate, unmeasured.NetRxRateOK = 0, false
	m.ready(id, unmeasured.ID)
	m.evalResourceRules(ctx, snapOf(unmeasured), hostsOf(unmeasured))
	if evs := events(t, st, ctx); len(evs) != 1 {
		t.Fatalf("an unmeasured poll must not resolve an ongoing condition, got %d events:\n%s", len(evs), dump(evs))
	}
	if states, _ := st.ListAlertStates(ctx); len(states) != 1 {
		t.Errorf("the condition must survive an unmeasured poll, %d states left", len(states))
	}

	// Poll 3: sampling recovers, still over threshold — must stay silent
	// (still the SAME incident), not fire a second time.
	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))
	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("recovery of an unchanged, still-over-threshold condition must stay quiet, got %d events:\n%s", len(evs), dump(evs))
	}
}

// TestNetworkThroughputBelowRuleIgnoresUnmeasuredPolls: a "<" rule (e.g.
// "alert if throughput drops below X", used to detect a stalled feed) must
// not accumulate dwell time or fire off an unmeasured poll's invented zero.
func TestNetworkThroughputBelowRuleIgnoresUnmeasuredPolls(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id, err := st.CreateAlertRule(ctx, &store.AlertRule{
		Name: "RX stalled", Enabled: true, Type: "resource", Target: "",
		Config: `{"metric":"netrx_rate","op":"<","threshold":1000,"durationSec":30}`, Severity: "warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := netContainer() // NetRxRateOK is false by construction — unmeasured

	for i := 0; i < 3; i++ {
		m.evalResourceRules(ctx, snapOf(cs), hostsOf(cs))
	}

	if evs := events(t, st, ctx); len(evs) != 0 {
		t.Fatalf("repeated unmeasured polls must never fire a '<' rule off an invented zero, got %d:\n%s", len(evs), dump(evs))
	}
	if _, ok := m.overSince.Load(ruleKey(id, cs.ID)); ok {
		t.Error("an unmeasured poll must not start (or keep) the dwell-time clock")
	}
}
