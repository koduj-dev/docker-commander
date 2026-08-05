package history

import (
	"context"
	"testing"
	"time"
)

// A container that stops being sampled must not keep its series for ever.
//
// Trimming only ran for the containers in the current batch, so a deleted
// container held its whole retention window — seven metrics × up to six hours at
// 15s is ~10k points — plus its hosts entry, until the process restarted.
func TestMemoryStoreForgetsContainersThatStopReporting(t *testing.T) {
	m := newMemoryStore(time.Hour)
	ctx := context.Background()
	old := time.Now().Add(-3 * time.Hour)

	if err := m.Record(ctx, []Sample{{ContainerID: "gone", HostID: 1, Time: old, CPU: 5}}); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	_, present := m.series["gone"]
	m.mu.RUnlock()
	if !present {
		t.Fatal("the sample should have been recorded")
	}

	// A later batch for a different container sweeps the stale one.
	if err := m.Record(ctx, []Sample{{ContainerID: "live", HostID: 1, Time: time.Now(), CPU: 7}}); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	_, staleLeft := m.series["gone"]
	_, hostLeft := m.hosts["gone"]
	_, liveLeft := m.series["live"]
	m.mu.RUnlock()

	if staleLeft || hostLeft {
		t.Error("a container that stopped reporting should be forgotten once its points expire")
	}
	if !liveLeft {
		t.Error("the container still reporting must be kept")
	}
}

// The counterweight: a container that is merely quiet for a moment — but whose
// points are still inside the window — must survive.
func TestMemoryStoreKeepsRecentlySeenContainers(t *testing.T) {
	m := newMemoryStore(time.Hour)
	ctx := context.Background()

	if err := m.Record(ctx, []Sample{{ContainerID: "quiet", HostID: 1, Time: time.Now().Add(-time.Minute), CPU: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Record(ctx, []Sample{{ContainerID: "busy", HostID: 1, Time: time.Now(), CPU: 9}}); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	_, kept := m.series["quiet"]
	m.mu.RUnlock()
	if !kept {
		t.Error("a container sampled a minute ago is not stale")
	}
}
