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

func TestMemoryStoreQueryAll(t *testing.T) {
	m := newMemoryStore(time.Hour)
	ctx := context.Background()
	now := time.Now()

	if err := m.Record(ctx, []Sample{{ContainerID: "a", HostID: 1, Time: now, NetRx: 100}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Record(ctx, []Sample{{ContainerID: "b", HostID: 1, Time: now, NetRx: 200}}); err != nil {
		t.Fatal(err)
	}
	// c never reports — must be absent from the result, not an empty slice.

	out, err := m.QueryAll(ctx, MetricNetRx, now.Add(-time.Minute), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected exactly the 2 ids with data, got %d: %+v", len(out), out)
	}
	if _, ok := out["c"]; ok {
		t.Error("a container with no points must be absent, not an empty slice")
	}
	if len(out["a"]) != 1 || out["a"][0].V != 100 {
		t.Errorf("a's series wrong: %+v", out["a"])
	}
	if len(out["b"]) != 1 || out["b"][0].V != 200 {
		t.Errorf("b's series wrong: %+v", out["b"])
	}

	// An id present in the store but requested outside the window must not leak in.
	old, err := m.QueryAll(ctx, MetricNetRx, now.Add(time.Minute), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 0 {
		t.Errorf("a future 'since' should return nothing, got %+v", old)
	}

	// QueryAll must never return an id that wasn't in containerIDs, even if
	// the store holds data for it — this is the whole reason it takes an
	// explicit list instead of "everything".
	only := map[string]bool{"a": true}
	out2, err := m.QueryAll(ctx, MetricNetRx, now.Add(-time.Minute), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	for id := range out2 {
		if !only[id] {
			t.Errorf("QueryAll returned id %q that was not requested", id)
		}
	}
}
