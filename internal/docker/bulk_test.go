package docker

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestBoundedRunRespectsConcurrencyCap proves the concurrency bound itself,
// independent of Docker: run more work items than the cap and confirm the
// number running at once never exceeds it. No daemon needed, so this runs
// under -short (unlike the Docker-backed bulk tests in integration_test.go).
func TestBoundedRunRespectsConcurrencyCap(t *testing.T) {
	const n = 20
	const cap = 3

	var current, max int64
	boundedRun(n, cap, func(int) {
		cur := atomic.AddInt64(&current, 1)
		for {
			old := atomic.LoadInt64(&max)
			if cur <= old || atomic.CompareAndSwapInt64(&max, old, cur) {
				break
			}
		}
		// Long enough that overlapping goroutines are actually overlapping,
		// short enough the test stays fast.
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt64(&current, -1)
	})

	if max == 0 {
		t.Fatal("boundedRun never invoked fn — the test would pass vacuously")
	}
	if max > cap {
		t.Errorf("boundedRun exceeded its cap: observed %d concurrent, cap was %d", max, cap)
	}
}

// TestBoundedRunInvokesEveryIndexExactlyOnce guards against an off-by-one in
// the semaphore/WaitGroup bookkeeping dropping or duplicating work.
func TestBoundedRunInvokesEveryIndexExactlyOnce(t *testing.T) {
	const n = 37
	seen := make([]int32, n)
	boundedRun(n, 5, func(i int) {
		atomic.AddInt32(&seen[i], 1)
	})
	for i, c := range seen {
		if c != 1 {
			t.Errorf("index %d invoked %d times, want exactly 1", i, c)
		}
	}
}

// TestBoundedRunZeroItems guards the empty case: no goroutines, returns
// immediately, no panic on an empty semaphore drain.
func TestBoundedRunZeroItems(t *testing.T) {
	called := false
	boundedRun(0, 4, func(int) { called = true })
	if called {
		t.Error("boundedRun called fn with zero items")
	}
}

// TestBulkContainerActionEmptyIDs is the trivial shape case at the Manager
// level: no ids in, no results out, no panic.
func TestBulkContainerActionEmptyIDs(t *testing.T) {
	m := &Manager{}
	results := m.BulkContainerAction(nil, 0, nil, "stop") //nolint:staticcheck // nil ctx unused on the empty path
	if len(results) != 0 {
		t.Errorf("want 0 results for 0 ids, got %d", len(results))
	}
}
