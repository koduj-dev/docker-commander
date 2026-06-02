// Package monitor is the alerting engine. It watches container state (Docker
// events), resource usage (polled stats), restart frequency, and log output,
// evaluates user-defined rules, and dispatches matches to the in-app feed and
// configured webhooks. It also maintains a stats snapshot for the Prometheus
// exporter.
package monitor

import (
	"context"
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
	statsInterval   = 5 * time.Second
	logReconcileInt = 10 * time.Second
)

// ContainerStat is the cached per-container snapshot used by the exporter.
type ContainerStat struct {
	HostID     int64
	HostName   string
	ID         string
	Name       string
	State      string
	CPUPercent float64
	MemBytes   uint64
	MemPercent float64
}

// monitoredHosts returns the hosts the engine should watch (all configured
// hosts; falls back to the default local host id 0 if listing fails).
func (m *Monitor) monitoredHosts(ctx context.Context) []store.Host {
	hosts, err := m.store.ListHosts(ctx)
	if err != nil || len(hosts) == 0 {
		return []store.Host{{ID: 0, Name: "local"}}
	}
	return hosts
}

// Monitor is the long-running alert engine.
type Monitor struct {
	store   *store.Store
	docker  *docker.Manager
	history history.Store

	mu       sync.RWMutex
	snapshot map[string]ContainerStat

	cooldowns sync.Map // "ruleID:cid" -> time.Time (last fired)
	overSince sync.Map // "ruleID:cid" -> time.Time (resource threshold first crossed)

	restartMu sync.Mutex
	restarts  map[string][]time.Time // container id -> recent start timestamps

	logMu      sync.Mutex
	logCancels map[string]context.CancelFunc // "ruleID:cid" -> cancel

	dispatcher *dispatcher
}

// New builds a Monitor. hist may be nil to disable history recording.
func New(st *store.Store, dm *docker.Manager, hist history.Store) *Monitor {
	return &Monitor{
		store:      st,
		docker:     dm,
		history:    hist,
		snapshot:   make(map[string]ContainerStat),
		restarts:   make(map[string][]time.Time),
		logCancels: make(map[string]context.CancelFunc),
		dispatcher: newDispatcher(st),
	}
}

// Run starts all background loops and blocks until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); m.statsLoop(ctx) }()
	go func() { defer wg.Done(); m.watchManagerLoop(ctx) }()
	go func() { defer wg.Done(); m.logReconcileLoop(ctx) }()
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
	t := time.NewTicker(statsInterval)
	defer t.Stop()
	m.pollStats(ctx) // prime immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.pollStats(ctx)
		}
	}
}

func (m *Monitor) pollStats(ctx context.Context) {
	next := make(map[string]ContainerStat)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // bound concurrent stats calls

	// Sample every configured host so alerts and history cover them all.
	for _, h := range m.monitoredHosts(ctx) {
		containers, err := m.docker.ListContainers(ctx, h.ID)
		if err != nil {
			continue
		}
		for _, c := range containers {
			cs := ContainerStat{HostID: h.ID, HostName: h.Name, ID: c.ID, Name: c.Name, State: c.State}
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
					cs.CPUPercent = s.CPUPercent
					cs.MemBytes = s.MemUsage
					cs.MemPercent = s.MemPercent
				}
				mu.Lock()
				next[cs.ID] = cs
				mu.Unlock()
			}(cs)
		}
	}
	wg.Wait()

	m.mu.Lock()
	m.snapshot = next
	m.mu.Unlock()

	m.recordHistory(ctx, next)
	m.evalResourceRules(ctx, next)
}

// recordHistory persists the running containers' samples for charting.
func (m *Monitor) recordHistory(ctx context.Context, snap map[string]ContainerStat) {
	if m.history == nil {
		return
	}
	now := time.Now()
	samples := make([]history.Sample, 0, len(snap))
	for _, cs := range snap {
		if cs.State != "running" {
			continue
		}
		samples = append(samples, history.Sample{
			ContainerID: cs.ID, Time: now,
			CPU: cs.CPUPercent, MemPercent: cs.MemPercent, MemBytes: float64(cs.MemBytes),
		})
	}
	if len(samples) > 0 {
		if err := m.history.Record(ctx, samples); err != nil {
			log.Printf("monitor: record history: %v", err)
		}
	}
}

func (m *Monitor) evalResourceRules(ctx context.Context, snap map[string]ContainerStat) {
	rules, err := m.store.ListAlertRules(ctx)
	if err != nil {
		return
	}
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
			val := cs.CPUPercent
			if cfg.Metric == "mem" {
				val = cs.MemPercent
			}
			key := ruleKey(r.ID, cs.ID)
			if cfg.exceeds(val) {
				since, _ := m.overSince.LoadOrStore(key, time.Now())
				if time.Since(since.(time.Time)) >= time.Duration(cfg.DurationSec)*time.Second {
					v := val
					m.fire(ctx, r, cs.HostID, cs.HostName, cs.ID, cs.Name,
						sprintf("%s %.1f%% %s %.0f%% for %ds", strings.ToUpper(cfg.Metric), val, cfg.Op, cfg.Threshold, cfg.DurationSec), &v)
				}
			} else {
				m.overSince.Delete(key)
			}
		}
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
func (m *Monitor) watchHost(ctx context.Context, hostID int64, hostName string) {
	for ctx.Err() == nil {
		err := m.docker.WatchEvents(ctx, hostID, func(e docker.Event) {
			m.handleEvent(ctx, hostID, hostName, e)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("monitor: events stream for host %d ended: %v; retrying", hostID, err)
		}
		time.Sleep(2 * time.Second) // reconnect backoff
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
				m.fire(ctx, r, hostID, hostName, e.ContainerID, e.ContainerName, "container event: "+e.Action, nil)
			}
		case "restart":
			if e.Action == "start" || e.Action == "restart" {
				cfg, err := parseRestart(r.Config)
				if err == nil {
					if n := m.restartCount(e.ContainerID, cfg.WindowSec); n >= cfg.Count {
						v := float64(n)
						m.fire(ctx, r, hostID, hostName, e.ContainerID, e.ContainerName,
							sprintf("restarted %d times in %ds (possible crash loop)", n, cfg.WindowSec), &v)
					}
				}
			}
		}
	}
}

func (m *Monitor) recordRestart(cid string) {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	m.restarts[cid] = append(m.restarts[cid], time.Now())
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
				m.ensureFollower(ctx, key, lr.r, lr.cfg, h.ID, h.Name, c.ID, c.Name)
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

func (m *Monitor) ensureFollower(ctx context.Context, key string, r store.AlertRule, cfg logMatcher, hostID int64, hostName, cid, name string) {
	m.logMu.Lock()
	if _, ok := m.logCancels[key]; ok {
		m.logMu.Unlock()
		return
	}
	fctx, cancel := context.WithCancel(ctx)
	m.logCancels[key] = cancel
	m.logMu.Unlock()

	go func() {
		defer func() {
			m.logMu.Lock()
			delete(m.logCancels, key)
			m.logMu.Unlock()
		}()
		// tail "0": only match new lines, never the historical backlog.
		_ = m.docker.StreamLogs(fctx, hostID, cid, true, "0", func(l docker.LogLine) {
			if cfg.match(l.Message) {
				m.fire(fctx, r, hostID, hostName, cid, name, "log match: "+truncate(l.Message, 200), nil)
			}
		})
	}()
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

func (m *Monitor) fire(ctx context.Context, r store.AlertRule, hostID int64, hostName, cid, name, message string, value *float64) {
	key := ruleKey(r.ID, cid)
	cooldown := time.Duration(r.CooldownSec) * time.Second
	if last, ok := m.cooldowns.Load(key); ok {
		if time.Since(last.(time.Time)) < cooldown {
			return
		}
	}
	m.cooldowns.Store(key, time.Now())

	ev := &store.AlertEvent{
		RuleID: r.ID, RuleName: r.Name, Type: r.Type, Severity: r.Severity,
		HostID: hostID, HostName: hostName,
		ContainerID: cid, ContainerName: name, Message: message, Value: value,
	}
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.store.InsertAlertEvent(wctx, ev); err != nil {
		log.Printf("monitor: insert alert event: %v", err)
	}
	if r.WebhookID != nil {
		m.dispatcher.dispatch(*r.WebhookID, ev)
	}
	if r.Email {
		m.emailNotify(ev)
	}
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
