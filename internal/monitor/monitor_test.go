package monitor

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func newMonitor(t *testing.T) (*Monitor, *docker.Manager, *store.Store, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := crypto.New(key)
	st.SetCipher(c)
	_ = st.EnsureLocalHost(context.Background())
	dm := docker.NewManager(st)
	t.Cleanup(dm.Close)
	ctx := context.Background()
	if _, err := dm.SystemInfo(ctx, 0); err != nil {
		t.Skipf("docker daemon not available: %v", err)
	}
	hist := history.Open(ctx, history.Config{Retention: time.Hour})
	t.Cleanup(func() { hist.Close() })
	return New(st, dm, hist), dm, st, ctx
}

func startContainer(ctx context.Context, t *testing.T, dm *docker.Manager, name string) string {
	t.Helper()
	if err := dm.PullImage(ctx, 0, "alpine:latest", func(docker.PullProgress) {}); err != nil {
		// alpine is usually already present; ignore pull errors and try create.
	}
	id, err := dm.CreateContainer(ctx, 0, docker.CreateSpec{
		Image: "alpine:latest", Name: name, Cmd: []string{"sleep", "300"}, Start: true,
	})
	if err != nil {
		t.Skipf("cannot start test container: %v", err)
	}
	t.Cleanup(func() {
		if cli, err := dm.Client(ctx, 0); err == nil {
			_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}
	})
	return id
}

func TestMonitorPollAndFire(t *testing.T) {
	m, dm, st, ctx := newMonitor(t)
	name := "dctest_mon"
	id := startContainer(ctx, t, dm, name)

	// A resource rule (won't necessarily fire) + a state rule (we'll fire it).
	_, _ = st.CreateAlertRule(ctx, &store.AlertRule{Name: "cpu", Type: "resource", Enabled: true, CooldownSec: 1, Config: `{"metric":"cpu","op":">","threshold":0,"durationSec":0}`})
	_, _ = st.CreateAlertRule(ctx, &store.AlertRule{Name: "died", Type: "state", Enabled: true, CooldownSec: 1, Config: `{"events":["die"]}`})
	_, _ = st.CreateAlertRule(ctx, &store.AlertRule{Name: "logs", Type: "log", Enabled: true, CooldownSec: 1, Config: `{"pattern":"never-matches-xyz"}`})

	// pollStats samples the running container, records history and evaluates
	// resource rules.
	m.pollStats(ctx)
	snap := m.Snapshot()
	found := false
	for _, s := range snap {
		if s.Name == name {
			found = true
		}
	}
	if !found {
		t.Errorf("running container not in snapshot (%d entries)", len(snap))
	}

	// reconcileLogFollowers starts a follower for the running container, then
	// tear them down.
	m.reconcileLogFollowers(ctx)
	m.stopAllFollowers()

	// handleEvent on a "die" should fire the state rule and record an event.
	m.handleEvent(ctx, 0, "local", docker.Event{Action: "die", ContainerID: id, ContainerName: name})
	time.Sleep(200 * time.Millisecond) // fire writes async-ish via store
	evs, _, _ := st.ListAlertEvents(ctx, store.AlertQuery{Limit: 10})
	if len(evs) == 0 {
		t.Error("expected a fired state alert event")
	} else if evs[0].HostName != "local" {
		t.Errorf("alert event host_name = %q want local", evs[0].HostName)
	}

	// restart counting helpers
	m.recordRestart(id)
	m.recordRestart(id)
	if n := m.restartCount(id, 60); n != 2 {
		t.Errorf("restartCount = %d want 2", n)
	}
}

func TestMonitorMonitoredHostsFallback(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	m := New(st, docker.NewManager(st), nil)
	// No hosts configured → falls back to the default local host.
	hosts := m.monitoredHosts(context.Background())
	if len(hosts) != 1 || hosts[0].ID != 0 {
		t.Errorf("fallback host wrong: %+v", hosts)
	}
}

// TestNetRatesFromConsecutivePolls covers the throughput the dashboard shows.
//
// Docker only gives cumulative counters, so a rate is a difference — and the two
// cases that produce a confidently wrong number are a container seen for the
// first time (no previous sample) and a counter reset after a recreate.
func TestNetRatesFromConsecutivePolls(t *testing.T) {
	prev := map[string]ContainerStat{
		"a": {ID: "a", NetRx: 1000, NetTx: 500},
		"b": {ID: "b", NetRx: 9000, NetTx: 9000},
	}
	next := map[string]ContainerStat{
		"a": {ID: "a", NetRx: 3000, NetTx: 1500}, // +2000 / +1000
		"b": {ID: "b", NetRx: 10, NetTx: 20},     // recreated: counter restarted
		"c": {ID: "c", NetRx: 5000, NetTx: 5000}, // seen for the first time
	}

	applyNetRates(next, prev, 2.0) // two seconds between polls

	if got := next["a"].NetRxRate; got != 1000 {
		t.Errorf("rx rate = %v, want 1000 B/s (2000 bytes over 2s)", got)
	}
	if got := next["a"].NetTxRate; got != 500 {
		t.Errorf("tx rate = %v, want 500 B/s", got)
	}
	// A reset must read as no traffic, not as a negative rate and not as a spike
	// from the counters having wrapped.
	if got := next["b"].NetRxRate; got != 0 {
		t.Errorf("after a counter reset the rate should be 0, got %v", got)
	}
	// A container with no previous sample has no rate yet. Reporting its total as
	// a rate would show a brand-new container as the busiest thing on the host.
	if got := next["c"].NetRxRate; got != 0 {
		t.Errorf("a first-seen container should have no rate, got %v", got)
	}
}

func TestNetRatesIgnoreNonPositiveElapsed(t *testing.T) {
	prev := map[string]ContainerStat{"a": {ID: "a", NetRx: 100}}
	next := map[string]ContainerStat{"a": {ID: "a", NetRx: 200}}
	applyNetRates(next, prev, 0) // clock skew or a duplicate poll
	if next["a"].NetRxRate != 0 {
		t.Error("a zero interval must not produce a rate (it would be a division by zero)")
	}
}

// Every container start appends a timestamp, whether or not a restart rule cares.
// Pruning used to live only in the read path, which runs when a restart rule
// matches that container — so with no restart rules at all, a host with CI or
// cron churn accumulated timestamps until the process was restarted.
func TestRecordRestartPrunesItsOwnHistory(t *testing.T) {
	m := New(nil, nil, nil)

	m.restartMu.Lock()
	old := time.Now().Add(-2 * restartRetention)
	m.restarts["c1"] = []time.Time{old, old, old}
	m.restartMu.Unlock()

	m.recordRestart("c1")

	m.restartMu.Lock()
	got := len(m.restarts["c1"])
	m.restartMu.Unlock()
	if got != 1 {
		t.Errorf("stale timestamps should be dropped on record: %d kept, want just the new one", got)
	}
}

// The counterweight: timestamps inside the window are what a crash-loop rule
// counts, so pruning must not touch them.
func TestRecordRestartKeepsRecentHistory(t *testing.T) {
	m := New(nil, nil, nil)
	recent := time.Now().Add(-time.Minute)

	m.restartMu.Lock()
	m.restarts["c1"] = []time.Time{recent, recent}
	m.restartMu.Unlock()

	m.recordRestart("c1")

	m.restartMu.Lock()
	got := len(m.restarts["c1"])
	m.restartMu.Unlock()
	if got != 3 {
		t.Errorf("recent restarts must survive: %d kept, want 3", got)
	}
}
