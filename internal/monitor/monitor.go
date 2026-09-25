// Package monitor is the alerting engine. It watches container state (Docker
// events), resource usage (polled stats), restart frequency, and log output,
// evaluates user-defined rules, and dispatches matches to the in-app feed and
// configured webhooks. It also maintains a stats snapshot for the Prometheus
// exporter.
package monitor

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/store"
)

const (
	// defaultStatsInterval is how often we sample every container's stats in the
	// background. Each ContainerStats call costs ~1s (the daemon's collection
	// interval), so on a host with many containers this is a heavy sweep — keep
	// it infrequent enough not to keep the daemon busy (overview/charts read
	// this cached snapshot rather than re-sampling). Configurable via
	// DC_METRICS_INTERVAL → Monitor.SetStatsInterval.
	defaultStatsInterval = 15 * time.Second
	logReconcileInt      = 10 * time.Second

	// healthInterval is how often every monitored host is pinged for
	// reachability; healthTimeout bounds a single ping so a dead host can't
	// stall the sweep.
	healthInterval = 30 * time.Second
	healthTimeout  = 8 * time.Second
)

// HostHealth is the engine's cached view of one host's reachability. Since is
// when the current reachable/unreachable state began, so the UI can show how
// long a host has been down (or up).
type HostHealth struct {
	Reachable bool
	Since     time.Time
	LastCheck time.Time
	Err       string // last ping error when unreachable
}

// ContainerStat is the cached per-container snapshot used by the exporter.
type ContainerStat struct {
	HostID   int64
	HostName string
	ID       string
	Name     string
	State    string
	// Project is the container's compose project (stack) name, read from its
	// com.docker.compose.project label — "" for a container not managed by
	// Compose. Used to scope maintenance windows by stack; nothing else in
	// the engine reads it today.
	Project    string
	CPUPercent float64 // `docker stats` convention: 100% == one core
	CPUCores   float64 // cores the daemon reports, so CPUPercent can be normalised
	MemBytes   uint64
	MemLimit   uint64
	MemPercent float64 // of the container's limit, not of host RAM
	// Cumulative network counters, summed across the container's interfaces.
	// Kept raw so history can derive rates at read time.
	NetRx     uint64
	NetTx     uint64
	NetDrops  uint64 // rx+tx dropped packets
	NetErrors uint64 // rx+tx interface errors
	// Rates derived from the previous poll. Zero until a second sample exists,
	// and after a counter reset — a container that was just recreated has no
	// meaningful rate yet, and inventing one would be worse than showing none.
	NetRxRate float64 // bytes/s
	NetTxRate float64 // bytes/s
	// True only when applyNetRates actually computed the corresponding rate
	// THIS poll — a failed sample, a first-ever sample, or a counter reset
	// all leave the rate at its zero value but these at false. Without this,
	// a netrx_rate/nettx_rate resource rule couldn't tell "measured, genuinely
	// under threshold" from "couldn't measure it this poll", and the latter
	// would read as a legitimate recovery (see evalResourceRules' unmeasured
	// map) — turning one ongoing incident into a resolve/fire pair every time
	// a sample was missed.
	NetRxRateOK bool
	NetTxRateOK bool
	// Sampled is true only when this poll's SampleStats call for the
	// container actually succeeded. A container stays in the snapshot (it's
	// still running, per ListContainers) even when the stats call itself
	// times out or errors — every numeric field above is then just its zero
	// value, not a real reading. Without this flag that zero looked exactly
	// like a legitimate counter reset to recordHistory/applyNetRates, so a
	// single transient stats timeout on a busy container could read as "lost
	// everything, then gained it all back" the moment sampling recovered —
	// exactly the kind of spike the network rule type and Top Talkers are
	// supposed to tell apart from a real incident.
	Sampled bool
}

// metric returns the value a rule's metric names, and whether it is available.
// cpu_total divides by the core count so a threshold means the same thing on a
// 2-core box and a 64-core one.
func (cs ContainerStat) metric(name string) (float64, bool) {
	switch name {
	case "mem":
		return cs.MemPercent, true
	case "cpu_total":
		if cs.CPUCores <= 0 {
			return 0, false // without a core count the figure would be a guess
		}
		return cs.CPUPercent / cs.CPUCores, true
	case "netrx_rate":
		return cs.NetRxRate, cs.NetRxRateOK
	case "nettx_rate":
		return cs.NetTxRate, cs.NetTxRateOK
	default:
		return cs.CPUPercent, true
	}
}

// monitoredHosts returns the hosts the engine should watch (all configured
// hosts; falls back to the default local host id 0 if listing fails).
func (m *Monitor) monitoredHosts(ctx context.Context) []store.Host {
	hosts, err := m.store.ListHosts(ctx)
	if err != nil || len(hosts) == 0 {
		return []store.Host{{ID: 0, Name: "local"}}
	}
	// Skip hosts the operator has disabled — the monitor stops watching events
	// and sampling stats for them (e.g. a laptop that's offline).
	out := make([]store.Host, 0, len(hosts))
	for _, h := range hosts {
		if !h.Disabled {
			out = append(out, h)
		}
	}
	return out
}

// Monitor is the long-running alert engine.
type Monitor struct {
	store   *store.Store
	docker  *docker.Manager
	history history.Store

	mu         sync.RWMutex
	snapshot   map[string]ContainerStat
	snapshotAt time.Time

	cooldowns sync.Map // "ruleID:cid" -> time.Time (last fired)
	overSince sync.Map // "ruleID:cid" -> time.Time (resource threshold first crossed)

	restartMu sync.Mutex
	restarts  map[string][]time.Time // container id -> recent start timestamps

	logMu      sync.Mutex
	logCancels map[string]context.CancelFunc // "ruleID:cid" -> cancel

	healthMu sync.RWMutex
	health   map[int64]HostHealth // host id -> reachability

	statsInterval time.Duration // how often pollStats runs (default 15s)

	dispatcher *dispatcher
}

// New builds a Monitor. hist may be nil to disable history recording.
func New(st *store.Store, dm *docker.Manager, hist history.Store) *Monitor {
	return &Monitor{
		store:         st,
		docker:        dm,
		history:       hist,
		snapshot:      make(map[string]ContainerStat),
		restarts:      make(map[string][]time.Time),
		logCancels:    make(map[string]context.CancelFunc),
		health:        make(map[int64]HostHealth),
		statsInterval: defaultStatsInterval,
		dispatcher:    newDispatcher(st),
	}
}

// SetStatsInterval overrides how often the stats sweep runs. Values ≤ 0 are
// ignored (the default stands). Call before Run; it is not safe to change once
// the loop is running.
func (m *Monitor) SetStatsInterval(d time.Duration) {
	if d > 0 {
		m.statsInterval = d
	}
}

// Run starts all background loops and blocks until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(6)
	go func() { defer wg.Done(); m.statsLoop(ctx) }()
	go func() { defer wg.Done(); m.watchManagerLoop(ctx) }()
	go func() { defer wg.Done(); m.logReconcileLoop(ctx) }()
	go func() { defer wg.Done(); m.healthLoop(ctx) }()
	go func() { defer wg.Done(); m.retrySweepLoop(ctx) }()
	go func() { defer wg.Done(); m.maintenanceLogLoop(ctx) }()
	wg.Wait()
}

// Snapshot returns a copy of the latest per-container stats for the exporter.
func (m *Monitor) Snapshot() []ContainerStat {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ContainerStat, 0, len(m.snapshot))
	for _, s := range m.snapshot {
		out = append(out, s)
	}
	return out
}

// ---- stats polling + resource rules ----------------------------------------

func (m *Monitor) statsLoop(ctx context.Context) {
	t := time.NewTicker(m.statsInterval)
	defer t.Stop()
	// The first sweep can take a few seconds on a busy host; log around it so it
	// is clear when the app is fully up (and any later "loading" is a real error,
	// not just a not-yet-primed snapshot).
	log.Printf("monitor: building initial stats snapshot…")
	m.pollStats(ctx) // prime immediately
	if ctx.Err() == nil {
		log.Printf("monitor: stats snapshot ready (%d running containers) — fully up", m.runningInSnapshot())
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.pollStats(ctx)
		}
	}
}

// runningInSnapshot counts the running containers in the current snapshot.
func (m *Monitor) runningInSnapshot() int {
	n := 0
	for _, c := range m.Snapshot() {
		if c.State == "running" {
			n++
		}
	}
	return n
}

func (m *Monitor) pollStats(ctx context.Context) {
	next := make(map[string]ContainerStat)
	// Hosts whose container list actually came back. A host missing from here was
	// not observed this round — which is NOT the same as "nothing is wrong on it",
	// and the resolve sweep has to know the difference.
	sampled := make(map[int64]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // bound concurrent stats calls

	// Sample every configured host so alerts and history cover them all.
	for _, h := range m.monitoredHosts(ctx) {
		// Bound the per-host listing so one unreachable host can't stall the
		// whole stats poll (and every request behind it).
		lctx, lcancel := context.WithTimeout(ctx, 5*time.Second)
		containers, err := m.docker.ListContainers(lctx, h.ID)
		lcancel()
		if err != nil {
			continue
		}
		sampled[h.ID] = true
		for _, c := range containers {
			cs := ContainerStat{HostID: h.ID, HostName: h.Name, ID: c.ID, Name: c.Name, State: c.State, Project: c.Labels[docker.LabelComposeProject]}
			if c.State != "running" {
				mu.Lock()
				next[cs.ID] = cs
				mu.Unlock()
				continue
			}
			wg.Add(1)
			go func(cs ContainerStat) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
				defer cancel()
				if s, err := m.docker.SampleStats(sctx, cs.HostID, cs.ID); err == nil {
					cs.Sampled = true
					cs.CPUPercent = s.CPUPercent
					cs.CPUCores = s.CPUCores
					cs.MemBytes = s.MemUsage
					cs.MemLimit = s.MemLimit
					cs.MemPercent = s.MemPercent
					cs.NetRx = s.NetRx
					cs.NetTx = s.NetTx
					cs.NetDrops = s.NetRxDropped + s.NetTxDropped
					cs.NetErrors = s.NetRxErrors + s.NetTxErrors
				}
				mu.Lock()
				next[cs.ID] = cs
				mu.Unlock()
			}(cs)
		}
	}
	wg.Wait()

	// Derive per-container throughput from the previous poll. Docker only gives
	// cumulative counters, so a rate needs two samples; it is computed here, once,
	// rather than by every caller that wants it.
	now := time.Now()
	m.mu.Lock()
	if !m.snapshotAt.IsZero() {
		applyNetRates(next, m.snapshot, now.Sub(m.snapshotAt).Seconds())
	}
	m.snapshot = next
	m.snapshotAt = now
	m.mu.Unlock()

	m.recordHistory(ctx, next)
	m.evalResourceRules(ctx, next, sampled)
	m.evalNetworkRules(ctx, next)
}

// applyNetRates fills in per-container throughput from the previous poll.
//
// Split out so the cases that matter can be tested without a daemon: a normal
// delta, a counter reset (the container was recreated, so the counter
// restarted and the subtraction would go negative), a container seen for the
// first time — which has no rate at all, and whose cumulative total would
// otherwise be reported as one, making every new container look like the
// busiest thing on the host — and a failed sample on either side, which reads
// as all-zero counters and must not be diffed as if it were a real reading (a
// transient stats timeout would otherwise look exactly like a counter reset
// immediately followed by regaining everything at once).
func applyNetRates(next, prev map[string]ContainerStat, elapsed float64) {
	if elapsed <= 0 {
		return
	}
	for id, cs := range next {
		if !cs.Sampled {
			continue // this poll's counters for it are not real data
		}
		p, ok := prev[id]
		if !ok || !p.Sampled {
			continue // no reliable baseline to diff against
		}
		if cs.NetRx >= p.NetRx {
			cs.NetRxRate = float64(cs.NetRx-p.NetRx) / elapsed
			cs.NetRxRateOK = true
		}
		if cs.NetTx >= p.NetTx {
			cs.NetTxRate = float64(cs.NetTx-p.NetTx) / elapsed
			cs.NetTxRateOK = true
		}
		next[id] = cs
	}
}

// recordHistory persists the running containers' samples for charting.
//
// A container whose stats call failed this poll (Sampled false) is skipped
// entirely rather than recorded with all-zero counters — the network rule
// type and Top Talkers both derive an INCREASE from consecutive history
// points, and a real value dipping to zero for one point and recovering the
// next would otherwise look exactly like the traffic/drops the dip itself
// was hiding, firing a false alert or inflating a ranking off nothing more
// than a transient Docker API timeout.
func (m *Monitor) recordHistory(ctx context.Context, snap map[string]ContainerStat) {
	if m.history == nil {
		return
	}
	now := time.Now()
	samples := make([]history.Sample, 0, len(snap))
	for _, cs := range snap {
		if cs.State != "running" || !cs.Sampled {
			continue
		}
		samples = append(samples, history.Sample{
			ContainerID: cs.ID, HostID: cs.HostID, Time: now,
			CPU: cs.CPUPercent, MemPercent: cs.MemPercent, MemBytes: float64(cs.MemBytes),
			NetRx: float64(cs.NetRx), NetTx: float64(cs.NetTx),
			NetDrops: float64(cs.NetDrops), NetErrors: float64(cs.NetErrors),
		})
	}
	if len(samples) > 0 {
		if err := m.history.Record(ctx, samples); err != nil {
			log.Printf("monitor: record history: %v", err)
		}
	}
}

// evalResourceRules turns threshold rules into conditions with a lifetime.
//
// The old shape evaluated every rule against every container independently and
// notified whenever the cooldown allowed, which produced three problems in
// practice: two rules over one metric emitted two alerts for one fact, the same
// alert reappeared every cooldown with nothing having changed, and nothing was
// ever emitted when the problem went away.
//
// So the unit here is a CONDITION — one per (host, container, metric) — not a
// rule. Of the rules currently over threshold for a condition, only the most
// severe speaks; the rest are its context, not separate news. A condition is
// announced when it starts, again only if it changes severity or the re-notify
// interval elapses, and once more when it ends, carrying how long it lasted.
func (m *Monitor) evalResourceRules(ctx context.Context, snap map[string]ContainerStat, sampled map[int64]bool) {
	rules, err := m.store.ListAlertRules(ctx)
	if err != nil {
		return
	}
	states, err := m.store.ListAlertStates(ctx)
	if err != nil {
		return
	}
	prev := make(map[string]store.AlertState, len(states))
	for _, st := range states {
		prev[stateKey(st.HostID, st.ContainerID, st.Metric)] = st
	}

	// Winners: the most severe rule currently over threshold per condition.
	type candidate struct {
		rule  store.AlertRule
		cfg   resourceConfig
		stat  ContainerStat
		value float64
	}
	winners := map[string]candidate{}
	// Condition keys where a matching rule tried to read the metric this poll
	// but couldn't (cs.metric returned ok=false) — a missing core count, or a
	// netrx_rate/nettx_rate that has no rate yet (failed sample, first poll,
	// or a counter reset). The resolve sweep below must treat these the same
	// as "host not sampled": not winning is not the same as "measured and
	// found fine", and resolving here would turn one ongoing incident into a
	// resolve/fire pair every time a single poll couldn't read the metric.
	unmeasured := map[string]bool{}

	for _, r := range rules {
		if !r.Enabled || r.Type != "resource" {
			continue
		}
		cfg, err := parseResource(r.Config)
		if err != nil {
			continue
		}
		for _, cs := range snap {
			if cs.State != "running" || !matchTarget(r.Target, cs.Name) {
				continue
			}
			val, ok := cs.metric(cfg.Metric)
			if !ok {
				unmeasured[stateKey(cs.HostID, cs.ID, cfg.metricKey())] = true
				continue
			}
			key := ruleKey(r.ID, cs.ID)
			if !cfg.exceeds(val) {
				m.overSince.Delete(key)
				continue
			}
			// The dwell time stays per RULE: two rules over one metric can have
			// different durationSec, and each has to serve its own before it
			// gets a say in who wins.
			since, _ := m.overSince.LoadOrStore(key, time.Now())
			if time.Since(since.(time.Time)) < time.Duration(cfg.DurationSec)*time.Second {
				continue
			}
			ck := stateKey(cs.HostID, cs.ID, cfg.metricKey())
			if cur, ok := winners[ck]; ok && severityRank(cur.rule.Severity) >= severityRank(r.Severity) {
				continue
			}
			winners[ck] = candidate{rule: r, cfg: cfg, stat: cs, value: val}
		}
	}

	now := time.Now()
	for ck, c := range winners {
		msg := resourceMessage(c.cfg, c.stat, c.value)
		v := c.value
		st, existed := prev[ck]

		var suppressed, repeated bool
		switch {
		case !existed:
			suppressed = m.emit(ctx, c.rule, c.stat.HostID, c.stat.HostName, c.stat.ID, c.stat.Name, c.stat.Project,
				msg, &v, store.KindFiring, 0)
			st = store.AlertState{StartedAt: now}
		case severityRank(c.rule.Severity) > severityRank(st.Severity):
			suppressed = m.emit(ctx, c.rule, c.stat.HostID, c.stat.HostName, c.stat.ID, c.stat.Name, c.stat.Project,
				msg, &v, store.KindEscalated, int(now.Sub(st.StartedAt).Seconds()))
		case severityRank(c.rule.Severity) < severityRank(st.Severity):
			suppressed = m.emit(ctx, c.rule, c.stat.HostID, c.stat.HostName, c.stat.ID, c.stat.Name, c.stat.Project,
				msg, &v, store.KindEased, int(now.Sub(st.StartedAt).Seconds()))
		case c.rule.CooldownSec > 0 && now.Sub(st.NotifiedAt) >= time.Duration(c.rule.CooldownSec)*time.Second:
			// A zero NotifiedAt means nobody was ever told — the firing (and any
			// escalation since) was silenced by a maintenance window. Once that
			// window is over this is the FIRST real delivery, not a re-announcement,
			// so it goes out as a firing: a repeat is hidden in the feed by default,
			// which made the alert look like it had vanished when the window ended.
			//
			// While the window is still open there is nothing to do: emitting now
			// would record a silenced "firing" on every poll (the gate stays open
			// precisely because nothing was delivered). Just wait for it to end.
			kind := store.KindRepeat
			if st.NotifiedAt.IsZero() {
				if m.silencedNow(c.stat.HostID, c.stat.Project, c.stat.Name, c.rule) {
					m.saveState(ctx, c.stat, c.cfg, c.rule, &v, st.StartedAt, st.NotifiedAt)
					continue
				}
				kind = store.KindFiring
			}
			repeated = kind == store.KindRepeat
			suppressed = m.emit(ctx, c.rule, c.stat.HostID, c.stat.HostName, c.stat.ID, c.stat.Name, c.stat.Project,
				msg, &v, kind, int(now.Sub(st.StartedAt).Seconds()))
		default:
			// Still true, nothing changed, not yet time to repeat: say nothing.
			// This is the whole point — silence here is the feature.
			// Carry the previous notify time forward. Writing `now` here would
			// reset the re-notify clock on every evaluation, so a condition that
			// stayed quiet could never reach its repeat interval — the silence
			// would be permanent instead of bounded.
			m.saveState(ctx, c.stat, c.cfg, c.rule, &v, st.StartedAt, st.NotifiedAt)
			continue
		}
		// A suppressed emit didn't actually notify anyone. Keeping the
		// PREVIOUS NotifiedAt (zero, for a condition that just started)
		// instead of stamping `now` here means the repeat-interval check
		// above stays eligible to retry on the very next poll once any
		// active maintenance window ends — not after a full interval
		// measured from a notification nobody received.
		notifiedAt := now
		if suppressed && !(repeated && !st.NotifiedAt.IsZero()) {
			notifiedAt = st.NotifiedAt
		}
		// (A suppressed REPEAT of something that WAS delivered before does advance
		// the gate: nothing new is owed after the window, and leaving it open made
		// every poll re-evaluate — and log — the silenced repeat, not once per
		// cooldown.)
		m.saveState(ctx, c.stat, c.cfg, c.rule, &v, orNow(st.StartedAt, now), notifiedAt)
	}

	// Anything that was firing and no longer wins its condition has ended.
	for ck, st := range prev {
		if _, still := winners[ck]; still {
			continue
		}
		// Unless its host wasn't observed this round. A failed or timed-out
		// container listing means we know nothing about that host — and silence is
		// not recovery. Resolving here announced "MEM back to normal" for every
		// condition on a host that had merely become unreachable, then started the
		// incident over from zero when it came back, so a flaky link produced a
		// resolve/fire pair every poll. Disabling a host did the same.
		if !sampled[st.HostID] {
			continue
		}
		// The metric itself couldn't be read this poll (see unmeasured
		// above) — silence here is exactly as uninformative as an
		// unreachable host, not a signal the condition actually cleared.
		if unmeasured[ck] {
			continue
		}
		dur := int(now.Sub(st.StartedAt).Seconds())
		m.emit(ctx, store.AlertRule{
			ID: st.RuleID, Name: st.RuleName, Type: "resource", Severity: "info",
		}, st.HostID, st.HostName, st.ContainerID, st.ContainerName, snap[st.ContainerID].Project,
			sprintf("%s back to normal after %s", strings.ToUpper(st.Metric), humanDuration(dur)),
			nil, store.KindResolved, dur)
		_ = m.store.DeleteAlertState(ctx, st.HostID, st.ContainerID, st.Metric)
	}
}

// netRuleWindow identifies one (metric, window) combination network rules
// can share — the unit evalNetworkRules batches its history reads by.
type netRuleWindow struct {
	metric    string
	windowSec int
}

// evalNetworkRules fires "network" rules — packet drops/errors that grew by
// at least Threshold within the last WindowSec. Unlike evalResourceRules,
// this is NOT a condition with a lifetime: an "increase over a window" has
// no stable "current value" the way CPU% does (the same window, queried a
// second later, is a different window), so there is nothing to escalate,
// ease or resolve. It fires edge-triggered through m.fire, exactly like the
// "restart" rule type's crash-loop detection, just polled instead of
// event-driven, since there's no single Docker event a packet drop maps to.
//
// History reads are batched via QueryAll, one per distinct (metric, window)
// combination across ALL running containers, not one Query per rule ×
// container. A naive per-pair loop is O(rules × containers) synchronous
// round trips every 15s poll — with Redis history that is thousands of
// sequential round trips on a host with many rules and containers, all on
// the same goroutine that also has to finish before the next stats poll,
// history recording, and resource-rule evaluation can run.
func (m *Monitor) evalNetworkRules(ctx context.Context, snap map[string]ContainerStat) {
	if m.history == nil {
		return // no history store configured, nothing to query a window from
	}
	rules, err := m.store.ListAlertRules(ctx)
	if err != nil {
		return
	}
	now := time.Now()

	ids := make([]string, 0, len(snap))
	for _, cs := range snap {
		if cs.State == "running" {
			ids = append(ids, cs.ID)
		}
	}

	// attempted tracks every (metric, window) already queried THIS poll,
	// successful or not — series only holds the successful ones, so without
	// a separate "did we already try" record, a failing QueryAll (a Redis
	// timeout/outage) would be retried once per rule sharing that key
	// instead of once for the whole poll, exactly the N×1 regression this
	// batching exists to avoid.
	attempted := make(map[netRuleWindow]bool)
	series := make(map[netRuleWindow]map[string][]history.Point)
	for _, r := range rules {
		if !r.Enabled || r.Type != "network" {
			continue
		}
		cfg, err := parseNetwork(r.Config)
		if err != nil {
			continue
		}
		metric := history.MetricNetDrops
		if cfg.Metric == "neterrors" {
			metric = history.MetricNetErrors
		}
		key := netRuleWindow{metric: metric, windowSec: cfg.WindowSec}
		if !attempted[key] {
			attempted[key] = true
			pts, err := m.history.QueryAll(ctx, metric, now.Add(-time.Duration(cfg.WindowSec)*time.Second), ids)
			if err != nil {
				continue
			}
			series[key] = pts
		}
		byContainer, ok := series[key]
		if !ok {
			continue // this key's query failed this poll — already tried, don't retry
		}
		for _, cs := range snap {
			if cs.State != "running" || !matchTarget(r.Target, cs.Name) {
				continue
			}
			inc, ok := history.Increase(byContainer[cs.ID])
			if !ok || inc < cfg.Threshold {
				continue
			}
			v := inc
			m.fire(ctx, r, cs.HostID, cs.HostName, cs.ID, cs.Name, cs.Project,
				sprintf("%s increased by %.0f in the last %s", networkMetricLabel(cfg.Metric), inc, humanDuration(cfg.WindowSec)),
				&v)
		}
	}
}

// networkMetricLabel says what a network rule's metric actually counts.
func networkMetricLabel(metric string) string {
	if metric == "neterrors" {
		return "interface errors"
	}
	return "dropped packets"
}

// saveState persists a condition, preserving when it started. notifiedAt is
// deliberately NOT defaulted to now when zero — a zero value means "this
// condition has never actually been delivered" (its only emit so far was
// suppressed by a maintenance window), and callers rely on that to keep the
// repeat-interval check eligible to retry as soon as any window ends.
func (m *Monitor) saveState(ctx context.Context, cs ContainerStat, cfg resourceConfig,
	r store.AlertRule, value *float64, startedAt, notifiedAt time.Time,
) {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	_ = m.store.UpsertAlertState(ctx, &store.AlertState{
		HostID: cs.HostID, HostName: cs.HostName,
		ContainerID: cs.ID, ContainerName: cs.Name,
		Metric: cfg.metricKey(), RuleID: r.ID, RuleName: r.Name, Severity: r.Severity,
		LastValue: value, StartedAt: startedAt, NotifiedAt: notifiedAt,
	})
}

// orNow returns t, or fallback when t is the zero time.
func orNow(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

// severityRank orders severities so the loudest rule over a metric wins.
func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

// stateKey identifies a condition: a metric on a container on a host.
func stateKey(hostID int64, cid, metric string) string {
	return itoa(hostID) + ":" + cid + ":" + metric
}

// resourceMessage says what the number actually measures.
//
// "MEM 61.9% > 5%" left a reader guessing whether the percentage was of host RAM
// or of the container's limit, and a CPU figure above 100% looked like a bug
// rather than the `docker stats` convention. Both now state their basis and
// carry absolute values where they exist.
func resourceMessage(cfg resourceConfig, cs ContainerStat, val float64) string {
	switch cfg.Metric {
	case "mem":
		if cs.MemLimit > 0 {
			return sprintf("MEM %s / %s (%.1f%% of limit) %s %.0f%% for %ds",
				humanBytes(cs.MemBytes), humanBytes(cs.MemLimit), val, cfg.Op, cfg.Threshold, cfg.DurationSec)
		}
		return sprintf("MEM %s (%.1f%%) %s %.0f%% for %ds",
			humanBytes(cs.MemBytes), val, cfg.Op, cfg.Threshold, cfg.DurationSec)
	case "cpu_total":
		return sprintf("CPU %.1f%% of %.0f cores %s %.0f%% for %ds",
			val, cs.CPUCores, cfg.Op, cfg.Threshold, cfg.DurationSec)
	case "netrx_rate", "nettx_rate":
		dir := "RX"
		if cfg.Metric == "nettx_rate" {
			dir = "TX"
		}
		return sprintf("%s %s/s %s %s/s for %ds",
			dir, humanBytes(uint64(val)), cfg.Op, humanBytes(uint64(cfg.Threshold)), cfg.DurationSec)
	default:
		return sprintf("CPU %.1f%% of one core (%.0f cores available) %s %.0f%% for %ds",
			val, cs.CPUCores, cfg.Op, cfg.Threshold, cfg.DurationSec)
	}
}

// humanBytes renders a byte count for a human reading an alert.
func humanBytes(b uint64) string {
	const unit = 1024.0
	f := float64(b)
	if f < unit {
		return sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	for _, u := range units {
		f /= unit
		if f < unit {
			return sprintf("%.1f %s", f, u)
		}
	}
	return sprintf("%.1f EB", f)
}

// humanDuration renders how long a condition held.
func humanDuration(sec int) string {
	switch {
	case sec < 60:
		return sprintf("%ds", sec)
	case sec < 3600:
		return sprintf("%dm %ds", sec/60, sec%60)
	default:
		return sprintf("%dh %dm", sec/3600, (sec%3600)/60)
	}
}

// ---- docker events: state + restart rules -----------------------------------

// watchManagerLoop keeps one Docker-events watcher per configured host alive,
// starting watchers for newly added hosts and stopping them for removed ones.
func (m *Monitor) watchManagerLoop(ctx context.Context) {
	watchers := make(map[int64]context.CancelFunc) // hostID -> cancel
	defer func() {
		for _, cancel := range watchers {
			cancel()
		}
	}()
	t := time.NewTicker(logReconcileInt)
	defer t.Stop()
	for {
		hosts := m.monitoredHosts(ctx)
		seen := make(map[int64]bool, len(hosts))
		for _, h := range hosts {
			seen[h.ID] = true
			if _, ok := watchers[h.ID]; !ok {
				wctx, cancel := context.WithCancel(ctx)
				watchers[h.ID] = cancel
				go m.watchHost(wctx, h.ID, h.Name)
			}
		}
		for id, cancel := range watchers {
			if !seen[id] {
				cancel()
				delete(watchers, id)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// watchHost streams one host's Docker events until its context is cancelled.
// Reconnects use exponential backoff so an unreachable host (e.g. a laptop that
// went offline) doesn't spam the log or hammer the connection every couple of
// seconds. A stream that stayed up for a while before dropping is treated as a
// transient blip and resets the backoff.
func (m *Monitor) watchHost(ctx context.Context, hostID int64, hostName string) {
	const (
		minBackoff = 2 * time.Second
		maxBackoff = 2 * time.Minute
	)
	backoff := minBackoff
	for ctx.Err() == nil {
		start := time.Now()
		err := m.docker.WatchEvents(ctx, hostID, func(e docker.Event) {
			m.handleEvent(ctx, hostID, hostName, e)
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			backoff = minBackoff // a real connection that dropped — retry promptly
		}
		if err != nil {
			log.Printf("monitor: events stream for host %d ended: %v; retrying in %s", hostID, err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (m *Monitor) handleEvent(ctx context.Context, hostID int64, hostName string, e docker.Event) {
	if e.Action == "start" || e.Action == "restart" {
		m.recordRestart(e.ContainerID)
	}
	rules, err := m.store.ListAlertRules(ctx)
	if err != nil {
		return
	}
	for _, r := range rules {
		if !r.Enabled || !matchTarget(r.Target, e.ContainerName) {
			continue
		}
		switch r.Type {
		case "state":
			cfg, err := parseState(r.Config)
			if err == nil && cfg.matches(e.Action) {
				m.fire(ctx, r, hostID, hostName, e.ContainerID, e.ContainerName, e.Project, "container event: "+e.Action, nil)
			}
		case "restart":
			if e.Action == "start" || e.Action == "restart" {
				cfg, err := parseRestart(r.Config)
				if err == nil {
					if n := m.restartCount(e.ContainerID, cfg.WindowSec); n >= cfg.Count {
						v := float64(n)
						m.fire(ctx, r, hostID, hostName, e.ContainerID, e.ContainerName, e.Project,
							sprintf("restarted %d times in %ds (possible crash loop)", n, cfg.WindowSec), &v)
					}
				}
			}
		}
	}
}

// restartRetention bounds how long a restart timestamp is worth keeping. Rules
// use windows of seconds to minutes; an hour is generous for any of them.
const restartRetention = time.Hour

func (m *Monitor) recordRestart(cid string) {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	now := time.Now()
	// Pruned here, not only in restartCount. Every container start appends,
	// regardless of whether any rule cares — but the pruning lived in the read
	// path, which only runs when a restart rule matches that container. With no
	// restart rules (or one targeting a different name) a host with CI or cron
	// churn accumulated timestamps until the process was restarted.
	cutoff := now.Add(-restartRetention)
	kept := m.restarts[cid][:0]
	for _, t := range m.restarts[cid] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	m.restarts[cid] = append(kept, now)
}

func (m *Monitor) restartCount(cid string, windowSec int) int {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	cutoff := time.Now().Add(-time.Duration(windowSec) * time.Second)
	kept := m.restarts[cid][:0]
	for _, t := range m.restarts[cid] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	m.restarts[cid] = kept
	return len(kept)
}

// ---- log following: log pattern rules ---------------------------------------

func (m *Monitor) logReconcileLoop(ctx context.Context) {
	t := time.NewTicker(logReconcileInt)
	defer t.Stop()
	m.reconcileLogFollowers(ctx)
	for {
		select {
		case <-ctx.Done():
			m.stopAllFollowers()
			return
		case <-t.C:
			m.reconcileLogFollowers(ctx)
		}
	}
}

func (m *Monitor) reconcileLogFollowers(ctx context.Context) {
	rules, err := m.store.ListAlertRules(ctx)
	if err != nil {
		return
	}
	// Collect log rules once; then walk every host's running containers.
	type logRule struct {
		r   store.AlertRule
		cfg logMatcher
	}
	var logRules []logRule
	for _, r := range rules {
		if !r.Enabled || r.Type != "log" {
			continue
		}
		if cfg, err := parseLog(r.Config); err == nil {
			logRules = append(logRules, logRule{r, cfg})
		}
	}

	want := make(map[string]struct{}) // keys that should be running
	for _, h := range m.monitoredHosts(ctx) {
		if len(logRules) == 0 {
			break
		}
		containers, err := m.docker.ListContainers(ctx, h.ID)
		if err != nil {
			continue
		}
		for _, lr := range logRules {
			for _, c := range containers {
				if c.State != "running" || !matchTarget(lr.r.Target, c.Name) {
					continue
				}
				key := ruleKey(lr.r.ID, c.ID)
				want[key] = struct{}{}
				m.ensureFollower(ctx, key, lr.r, lr.cfg, h.ID, h.Name, c.ID, c.Name, c.Labels[docker.LabelComposeProject])
			}
		}
	}

	// Stop followers no longer wanted.
	m.logMu.Lock()
	for key, cancel := range m.logCancels {
		if _, ok := want[key]; !ok {
			cancel()
			delete(m.logCancels, key)
		}
	}
	m.logMu.Unlock()
}

func (m *Monitor) ensureFollower(ctx context.Context, key string, r store.AlertRule, cfg logMatcher, hostID int64, hostName, cid, name, project string) {
	m.logMu.Lock()
	if _, ok := m.logCancels[key]; ok {
		m.logMu.Unlock()
		return
	}
	fctx, cancel := context.WithCancel(ctx)
	m.logCancels[key] = cancel
	m.logMu.Unlock()

	go runFollower(cancel, func() {
		m.logMu.Lock()
		delete(m.logCancels, key)
		m.logMu.Unlock()
	}, func() error {
		// tail "0": only match new lines, never the historical backlog.
		return m.docker.StreamLogs(fctx, hostID, cid, true, "0", func(l docker.LogLine) {
			if cfg.match(l.Message) {
				m.fire(fctx, r, hostID, hostName, cid, name, project, "log match: "+truncate(l.Message, 200), nil)
			}
		})
	})
}

// runFollower streams until it ends, then cancels its context and cleans up.
//
// Extracted so the cancel can be asserted: forgetting it is invisible from the
// outside — the follower's entry disappears either way — while leaving a child
// context attached to the monitor's long-lived root for the life of the process.
// One per follower that ends on its own, which on a churning host is every few
// minutes.
func runFollower(cancel context.CancelFunc, cleanup func(), stream func() error) {
	defer func() {
		cancel()
		cleanup()
	}()
	_ = stream()
}

func (m *Monitor) stopAllFollowers() {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	for key, cancel := range m.logCancels {
		cancel()
		delete(m.logCancels, key)
	}
}

// ---- firing -----------------------------------------------------------------

// fire is the edge-triggered path: a container died, a log line matched, a
// restart loop tripped. Those have no "still true" and no "stopped being true",
// so they keep the plain cooldown — it is the only thing standing between a
// flapping container and one notification per event.
//
// Level-triggered threshold rules go through evalResourceRules and emit
// directly, because a cooldown is the wrong tool for a condition that persists:
// it turns "still broken" into a fresh alarm every interval.
func (m *Monitor) fire(ctx context.Context, r store.AlertRule, hostID int64, hostName, cid, name, project, message string, value *float64) {
	key := ruleKey(r.ID, cid)
	cooldown := time.Duration(r.CooldownSec) * time.Second
	if last, ok := m.cooldowns.Load(key); ok {
		if time.Since(last.(time.Time)) < cooldown {
			return
		}
	}
	// Only a REAL delivery consumes the cooldown. A suppressed emit (an
	// active maintenance window matched) must not — otherwise the next
	// genuine event of this rule+container, occurring right after the window
	// ends, would be silently dropped by a cooldown that only "fired"
	// because of a notification nobody actually got.
	if !m.emit(ctx, r, hostID, hostName, cid, name, project, message, value, store.KindFiring, 0) {
		m.cooldowns.Store(key, time.Now())
	}
}

// emit records an alert event and delivers it, returning whether an active
// maintenance window suppressed that delivery. kind says where in a
// condition's life this is; durationSec is how long it had been going.
// project is the container's compose project (stack), resolved by the
// caller from whatever source is immediately available to it (a Docker
// event's own actor attributes, ListContainers' labels, or the stats
// snapshot) — NOT re-derived here from the snapshot, which can lag a
// container created between stats polls by up to the poll interval.
func (m *Monitor) emit(ctx context.Context, r store.AlertRule, hostID int64, hostName, cid, name, project, message string,
	value *float64, kind string, durationSec int,
) bool {
	severity := r.Severity
	if severity == "" {
		severity = "info"
	}
	ev := &store.AlertEvent{
		RuleID: r.ID, RuleName: r.Name, Type: r.Type, Severity: r.Severity,
		HostID: hostID, HostName: hostName,
		ContainerID: cid, ContainerName: name, Project: project, Message: message, Value: value,
		Kind: kind, DurationSec: durationSec,
	}
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A silence stops paging, not observing: the event is recorded regardless of
	// an active maintenance window, so this check decides whether delivery below
	// runs — with one exception, a silenced repeat, handled just below.
	var win *store.MaintenanceWindow
	if w, err := m.store.FindActiveMaintenanceWindow(wctx, hostID, project, name, r.ID, severity, time.Now()); err != nil {
		log.Printf("monitor: check maintenance window: %v", err)
	} else if w != nil {
		win = w
		ev.Suppressed = true
		ev.SuppressedBy = w.ID
	}
	// Every fired alert goes to the process log (stderr) as one structured line.
	// Under systemd that lands in the journal — and from there in syslog or any
	// central collector — so alerts are visible beyond the in-app feed. It is
	// written after the window check so the same line says whether delivery was silenced — an operator reading the journal
	// or syslog otherwise sees an alert firing during maintenance with no hint
	// that nobody was paged.
	if win != nil {
		log.Printf("alert kind=%s severity=%s rule=%q host=%q container=%q message=%q silenced=true maintenance_window=%d window_name=%q",
			kind, severity, r.Name, hostName, name, message, win.ID, win.Name)
	} else {
		log.Printf("alert kind=%s severity=%s rule=%q host=%q container=%q message=%q",
			kind, severity, r.Name, hostName, name, message)
	}
	// A silenced REPEAT is not stored. The condition's firing event (and its
	// resolution) are already in the feed; a repeat only re-announces "still
	// true", which during a window nobody is being told anyway — and with
	// many containers on one rule it is what would fill the table (and the
	// database) for the whole window. The log line above still records it.
	if ev.Suppressed && kind == store.KindRepeat {
		return true
	}
	// The id is what delivery records attach to, so capture it before notifying.
	if id, err := m.store.InsertAlertEvent(wctx, ev); err != nil {
		log.Printf("monitor: insert alert event: %v", err)
	} else {
		ev.ID = id
	}
	if ev.Suppressed {
		return true
	}
	if r.WebhookID != nil {
		m.dispatcher.dispatch(*r.WebhookID, ev)
	}
	if r.Email {
		m.emailNotify(ev, r.Emails)
	}
	return false
}

// silencedNow reports whether a maintenance window would silence an alert of
// this rule for this container right now (same lookup emit does). A lookup
// error counts as "not silenced": emit will hit and log the same error, and
// failing towards delivery is the safe direction for an alert.
func (m *Monitor) silencedNow(hostID int64, project, name string, r store.AlertRule) bool {
	severity := r.Severity
	if severity == "" {
		severity = "info"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := m.store.FindActiveMaintenanceWindow(ctx, hostID, project, name, r.ID, severity, time.Now())
	return err == nil && w != nil
}

// ---- host reachability ------------------------------------------------------

// healthLoop pings every monitored host on a fixed interval and tracks
// reachability, firing an alert whenever a host transitions offline or recovers.
func (m *Monitor) healthLoop(ctx context.Context) {
	t := time.NewTicker(healthInterval)
	defer t.Stop()
	m.checkAllHosts(ctx) // probe once at startup so the state isn't empty
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.checkAllHosts(ctx)
		}
	}
}

// checkAllHosts pings each non-disabled host and records the result.
func (m *Monitor) checkAllHosts(ctx context.Context) {
	for _, h := range m.monitoredHosts(ctx) {
		pctx, cancel := context.WithTimeout(ctx, healthTimeout)
		err := m.docker.Ping(pctx, h.ID)
		cancel()
		m.recordHealth(h.ID, h.Name, err, time.Now())
	}
}

// recordHealth updates a host's cached reachability and fires an offline/recover
// alert on a state change. The first observation of a host never alerts — we
// report transitions, not the initial state — so a host that is already down at
// startup doesn't generate a spurious "went offline" alert.
func (m *Monitor) recordHealth(hostID int64, hostName string, pingErr error, now time.Time) {
	reachable := pingErr == nil
	errStr := ""
	if pingErr != nil {
		errStr = pingErr.Error()
	}

	m.healthMu.Lock()
	prev, existed := m.health[hostID]
	since := now
	if existed && prev.Reachable == reachable {
		since = prev.Since // unchanged → keep the original start time
	}
	m.health[hostID] = HostHealth{Reachable: reachable, Since: since, LastCheck: now, Err: errStr}
	transition := existed && prev.Reachable != reachable
	prevSince := prev.Since // captured under lock; only meaningful on a transition
	m.healthMu.Unlock()

	if transition {
		m.fireHostAlert(hostID, hostName, reachable, now.Sub(prevSince))
	}
}

// fireHostAlert records a host offline/recover event in the alert feed and, when
// SMTP is configured, emails it (honouring the host's per-host recipient). It is
// independent of user alert rules — host reachability is always watched.
func (m *Monitor) fireHostAlert(hostID int64, hostName string, online bool, downtime time.Duration) {
	severity := "critical"
	message := fmt.Sprintf("Host %q is unreachable", hostName)
	if online {
		severity = "info"
		message = fmt.Sprintf("Host %q recovered (was unreachable for %s)", hostName, downtime.Round(time.Second))
	}
	log.Printf("alert severity=%s rule=%q host=%q message=%q", severity, "Host reachability", hostName, message)
	_ = m.NotifySystem(&store.AlertEvent{
		RuleName: "Host reachability", Type: "host", Severity: severity,
		HostID: hostID, HostName: hostName, Message: message,
	})
}

// NotifySystem records and delivers a system-generated alert event that has
// no per-rule webhook/email configuration to key off — the same shape
// fireHostAlert used for host-reachability alerts before this was pulled out
// so other system-triggered notifications (e.g. an available image update)
// can reuse it instead of hand-rolling InsertAlertEvent + a
// maintenance-window check + emailNotify themselves. ev.HostID/Project/
// ContainerName/Severity are used to scope the maintenance-window lookup
// exactly like a rule-driven fire() does; ev.Suppressed/SuppressedBy are set
// on ev itself before it's inserted. Uses its own short-lived context rather
// than a caller-supplied one, so a slow/cancelled caller can't abort the
// write — the same reasoning fireHostAlert already relied on.
// NotifySystem's error return is nil once ev is durably recorded in the
// alert log (suppressed or not) — a caller with its own idempotency state
// keyed off "was this notified" (e.g. the image-update poller's dedup table)
// should only update that state once this returns nil; a non-nil error means
// the event was never recorded at all, so treating it as "notified" would
// permanently and silently lose the notification.
func (m *Monitor) NotifySystem(ev *store.AlertEvent) error {
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if win, err := m.store.FindActiveMaintenanceWindow(wctx, ev.HostID, ev.Project, ev.ContainerName, 0, ev.Severity, time.Now()); err != nil {
		log.Printf("monitor: check maintenance window: %v", err)
	} else if win != nil {
		ev.Suppressed = true
		ev.SuppressedBy = win.ID
	}
	id, insertErr := m.store.InsertAlertEvent(wctx, ev)
	if insertErr != nil {
		log.Printf("monitor: insert system alert event: %v", insertErr)
	} else {
		ev.ID = id
	}
	if !ev.Suppressed {
		// A system event isn't tied to a rule, so it uses the host/instance
		// recipients. Still attempted even if persistence failed, so a
		// transient DB error can't silently swallow a critical alert.
		m.emailNotify(ev, nil)
	}
	return insertErr
}

// HostHealth returns a snapshot of every tracked host's reachability, keyed by
// host id, for the API to surface in the hosts list. Hosts that have not been
// probed yet are absent (callers treat them as reachable/unknown).
func (m *Monitor) HostHealth() map[int64]HostHealth {
	m.healthMu.RLock()
	defer m.healthMu.RUnlock()
	out := make(map[int64]HostHealth, len(m.health))
	for k, v := range m.health {
		out[k] = v
	}
	return out
}

// ---- helpers ----------------------------------------------------------------

func matchTarget(target, name string) bool {
	target = strings.TrimSpace(target)
	if target == "" || target == "*" {
		return true
	}
	return strings.Contains(name, target)
}

func ruleKey(ruleID int64, cid string) string {
	return strings.Join([]string{itoa(ruleID), cid}, ":")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// compiledLog caches a compiled regexp for a log rule's lifetime within a call.
type logMatcher struct {
	substr string
	re     *regexp.Regexp
}

func (lm logMatcher) match(s string) bool {
	if lm.re != nil {
		return lm.re.MatchString(s)
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(lm.substr))
}
