package monitor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Tests for threshold alerts as conditions with a lifetime.
//
// The behaviour under test is mostly about what is NOT emitted: a condition that
// is still true and unchanged must produce silence, and two rules noticing one
// fact must produce one alert. Both are easy to regress into "notify again",
// which is what the engine did before and why an hour of the alert log was the
// same seven lines repeated.
//
// These need no Docker daemon — evalResourceRules reads a snapshot and the store
// — so they run under -short, where they will actually be seen.

func newAlertMonitor(t *testing.T) (*Monitor, *store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil, nil), st, context.Background()
}

// addRule stores a resource rule and returns its id.
func addRule(t *testing.T, st *store.Store, ctx context.Context, name, severity string, threshold float64, metric string, cooldown int) int64 {
	t.Helper()
	id, err := st.CreateAlertRule(ctx, &store.AlertRule{
		Name: name, Enabled: true, Type: "resource", Target: "",
		Config:   `{"metric":"` + metric + `","op":">","threshold":` + trimFloat(threshold) + `,"durationSec":30}`,
		Severity: severity, CooldownSec: cooldown,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func trimFloat(f float64) string { return sprintf("%.0f", f) }

// ready pre-ages a rule's dwell timer so evaluation doesn't have to wait out
// durationSec in real time.
func (m *Monitor) ready(ruleID int64, cid string) {
	m.overSince.Store(ruleKey(ruleID, cid), time.Now().Add(-time.Hour))
}

func snapOf(cs ContainerStat) map[string]ContainerStat {
	return map[string]ContainerStat{cs.ID: cs}
}

func events(t *testing.T, st *store.Store, ctx context.Context) []store.AlertEvent {
	t.Helper()
	evs, err := st.ListAlertEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func busyContainer() ContainerStat {
	return ContainerStat{
		HostID: 0, HostName: "local", ID: "c1", Name: "cache-redis", State: "running",
		CPUPercent: 40, CPUCores: 4, MemBytes: 3 << 30, MemLimit: 5 << 30, MemPercent: 60,
	}
}

// TestAlertConditionIsAnnouncedOnceThenGoesQuiet is the core of the change: the
// same condition, evaluated repeatedly, must not keep talking.
func TestAlertConditionIsAnnouncedOnceThenGoesQuiet(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id := addRule(t, st, ctx, "Memory", "warning", 5, "mem", 0)
	cs := busyContainer()

	for i := 0; i < 3; i++ {
		m.ready(id, cs.ID)
		m.evalResourceRules(ctx, snapOf(cs))
	}

	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("three evaluations of one unchanged condition produced %d events, want 1:\n%s", len(evs), dump(evs))
	}
	if evs[0].Kind != store.KindFiring {
		t.Errorf("first event kind = %q, want %q", evs[0].Kind, store.KindFiring)
	}
}

// TestAlertOverlappingRulesEmitOnlyTheMostSevere pins the dedup: one metric over
// two thresholds is one fact, and the louder rule is the one that speaks.
func TestAlertOverlappingRulesEmitOnlyTheMostSevere(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	// The CRITICAL rule is created first on purpose. Rules are evaluated in id
	// order, so if the winner were simply "the last one seen" this test would
	// still pass with the severity comparison deleted — it did, until a mutation
	// run caught it. Ordered this way, only severity can produce the right answer.
	crit := addRule(t, st, ctx, "Memory critical", "critical", 10, "mem", 0)
	warn := addRule(t, st, ctx, "Memory warn", "warning", 5, "mem", 0)
	cs := busyContainer()

	m.ready(warn, cs.ID)
	m.ready(crit, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 1 {
		t.Fatalf("two rules over one metric produced %d events, want 1:\n%s", len(evs), dump(evs))
	}
	if evs[0].Severity != "critical" {
		t.Errorf("severity = %q, want the most severe rule (critical)", evs[0].Severity)
	}
	if evs[0].RuleName != "Memory critical" {
		t.Errorf("rule = %q, want %q", evs[0].RuleName, "Memory critical")
	}
}

// TestAlertResolvesWhenTheConditionEnds covers the half that was missing
// entirely: without it there is no way to know a problem stopped, or how long it
// lasted.
func TestAlertResolvesWhenTheConditionEnds(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id := addRule(t, st, ctx, "Memory", "warning", 5, "mem", 0)
	cs := busyContainer()

	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs))

	// Backdate the incident so the resolved event carries a measurable duration.
	// It has to be deleted and re-inserted: UpsertAlertState deliberately never
	// updates started_at, which is what keeps an incident's clock running across
	// escalation and re-notify. (Found by this test failing with duration 0.)
	states, _ := st.ListAlertStates(ctx)
	if len(states) != 1 {
		t.Fatalf("expected one live condition, got %d", len(states))
	}
	if err := st.DeleteAlertState(ctx, states[0].HostID, states[0].ContainerID, states[0].Metric); err != nil {
		t.Fatal(err)
	}
	states[0].StartedAt = time.Now().Add(-90 * time.Second)
	if err := st.UpsertAlertState(ctx, &states[0]); err != nil {
		t.Fatal(err)
	}

	calm := cs
	calm.MemPercent = 1 // back under the threshold
	m.evalResourceRules(ctx, snapOf(calm))

	evs := events(t, st, ctx)
	if len(evs) != 2 {
		t.Fatalf("expected firing + resolved, got %d:\n%s", len(evs), dump(evs))
	}
	res := evs[0] // newest first
	if res.Kind != store.KindResolved {
		t.Fatalf("newest event kind = %q, want %q", res.Kind, store.KindResolved)
	}
	if res.DurationSec < 80 {
		t.Errorf("resolved duration = %ds, want roughly 90", res.DurationSec)
	}
	if !strings.Contains(res.Message, "back to normal") {
		t.Errorf("resolved message should say the condition ended: %q", res.Message)
	}
	if left, _ := st.ListAlertStates(ctx); len(left) != 0 {
		t.Errorf("resolved condition should be cleared, %d left", len(left))
	}
}

// TestAlertEscalationStaysOneIncident: crossing a higher threshold must change
// the existing condition, not open a second one alongside it.
func TestAlertEscalationStaysOneIncident(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	warn := addRule(t, st, ctx, "Memory warn", "warning", 50, "mem", 0)
	crit := addRule(t, st, ctx, "Memory critical", "critical", 80, "mem", 0)

	cs := busyContainer()
	cs.MemPercent = 60 // over warning only
	m.ready(warn, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs))

	before, _ := st.ListAlertStates(ctx)
	if len(before) != 1 {
		t.Fatalf("expected one condition, got %d", len(before))
	}
	started := before[0].StartedAt

	cs.MemPercent = 90 // now over critical too
	m.ready(warn, cs.ID)
	m.ready(crit, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 2 {
		t.Fatalf("expected firing + escalated, got %d:\n%s", len(evs), dump(evs))
	}
	if evs[0].Kind != store.KindEscalated {
		t.Errorf("newest event kind = %q, want %q", evs[0].Kind, store.KindEscalated)
	}
	after, _ := st.ListAlertStates(ctx)
	if len(after) != 1 {
		t.Fatalf("escalation must not open a second condition, got %d", len(after))
	}
	if !after[0].StartedAt.Equal(started) {
		t.Errorf("escalation restarted the incident clock: %v -> %v", started, after[0].StartedAt)
	}
	if after[0].Severity != "critical" {
		t.Errorf("condition severity = %q, want critical", after[0].Severity)
	}
}

// TestAlertRepeatsOnlyAfterTheInterval: a persistent condition re-notifies on the
// rule's own schedule, not on the evaluation loop's.
func TestAlertRepeatsOnlyAfterTheInterval(t *testing.T) {
	m, st, ctx := newAlertMonitor(t)
	id := addRule(t, st, ctx, "Memory", "warning", 5, "mem", 300)
	cs := busyContainer()

	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs))

	// Still true a moment later: silence.
	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs))
	if evs := events(t, st, ctx); len(evs) != 1 {
		t.Fatalf("within the interval the condition should stay quiet, got %d events:\n%s", len(evs), dump(evs))
	}

	// Backdate the last notification past the interval.
	states, _ := st.ListAlertStates(ctx)
	states[0].NotifiedAt = time.Now().Add(-10 * time.Minute)
	if err := st.UpsertAlertState(ctx, &states[0]); err != nil {
		t.Fatal(err)
	}
	m.ready(id, cs.ID)
	m.evalResourceRules(ctx, snapOf(cs))

	evs := events(t, st, ctx)
	if len(evs) != 2 {
		t.Fatalf("after the interval the condition should repeat once, got %d:\n%s", len(evs), dump(evs))
	}
	if evs[0].Kind != store.KindRepeat {
		t.Errorf("kind = %q, want %q", evs[0].Kind, store.KindRepeat)
	}
}

// TestCPUTotalNormalisesByCoreCount: the reason a "> 80%" CPU rule fired forever
// on multi-core hosts.
func TestCPUTotalNormalisesByCoreCount(t *testing.T) {
	cs := ContainerStat{CPUPercent: 324.5, CPUCores: 4}

	raw, ok := cs.metric("cpu")
	if !ok || raw != 324.5 {
		t.Errorf("cpu should stay the docker-stats figure, got %v (ok=%v)", raw, ok)
	}
	total, ok := cs.metric("cpu_total")
	if !ok {
		t.Fatal("cpu_total should be available when the core count is known")
	}
	if want := 324.5 / 4; total != want {
		t.Errorf("cpu_total = %v, want %v", total, want)
	}

	// The case from the report: app-php-fpm at 240.5% looked like a critical
	// breach of a >80% rule, but on a 4-core host that is 60% of the machine.
	// Under "cpu" it fires; under "cpu_total" it correctly does not.
	php := ContainerStat{CPUPercent: 240.5, CPUCores: 4}
	rawPHP, _ := php.metric("cpu")
	totalPHP, _ := php.metric("cpu_total")
	if !(rawPHP > 80) {
		t.Errorf("the raw docker-stats figure %.1f should exceed 80, that is the whole complaint", rawPHP)
	}
	if totalPHP > 80 {
		t.Errorf("240.5%% across 4 cores is %.1f%% of the machine — a >80%% cpu_total rule must not fire", totalPHP)
	}

	// Without a core count, normalising would be a guess; the metric is absent
	// rather than silently wrong.
	if _, ok := (ContainerStat{CPUPercent: 100}).metric("cpu_total"); ok {
		t.Error("cpu_total must be unavailable when the core count is unknown")
	}
}

// TestResourceMessageStatesItsBasis: "MEM 61.9% > 5%" never said 61.9% of what.
func TestResourceMessageStatesItsBasis(t *testing.T) {
	cs := busyContainer()
	cases := []struct {
		metric string
		val    float64
		want   []string
	}{
		{"mem", 61.9, []string{"3.0 GB", "5.0 GB", "of limit"}},
		{"cpu", 324.5, []string{"of one core", "4 cores available"}},
		{"cpu_total", 81.1, []string{"of 4 cores"}},
	}
	for _, c := range cases {
		t.Run(c.metric, func(t *testing.T) {
			cfg := resourceConfig{Metric: c.metric, Op: ">", Threshold: 5, DurationSec: 30}
			got := resourceMessage(cfg, cs, c.val)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("message %q should mention %q", got, want)
				}
			}
		})
	}
}

// dump renders events for a failure message.
func dump(evs []store.AlertEvent) string {
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(sprintf("  kind=%s severity=%s rule=%q msg=%q\n", e.Kind, e.Severity, e.RuleName, e.Message))
	}
	return b.String()
}
