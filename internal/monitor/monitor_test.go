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

func startContainer(t *testing.T, dm *docker.Manager, ctx context.Context, name string) string {
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
	id := startContainer(t, dm, ctx, name)

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
	evs, _ := st.ListAlertEvents(ctx, 10)
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
