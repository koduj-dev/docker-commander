package docker

import (
	"context"
	"sync"
)

// bulkActionConcurrency bounds how many container actions run at once for a
// bulk request, matching the pool-size convention used elsewhere in this
// package (statsConcurrency in overview.go, reused by probe.go too) rather
// than firing every container at the daemon in one burst.
const bulkActionConcurrency = 8

// BulkActionResult is the outcome of one container's action within a bulk
// request — one per requested id, in request order.
type BulkActionResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// BulkContainerAction runs action (restart|stop — callers must validate the
// action before calling this; ContainerAction itself rejects anything else)
// against each of ids, bounded to bulkActionConcurrency concurrent daemon
// calls. One container failing does not stop the others: every id gets a
// result, success or error, so the caller can report a per-container summary
// instead of a single aggregate ok/fail.
func (m *Manager) BulkContainerAction(ctx context.Context, hostID int64, ids []string, action string) []BulkActionResult {
	results := make([]BulkActionResult, len(ids))
	boundedRun(len(ids), bulkActionConcurrency, func(i int) {
		id := ids[i]
		if err := m.ContainerAction(ctx, hostID, id, action); err != nil {
			results[i] = BulkActionResult{ID: id, Error: err.Error()}
			return
		}
		results[i] = BulkActionResult{ID: id, OK: true}
	})
	return results
}

// boundedRun calls fn(i) for every i in [0,n), running at most concurrency
// invocations at a time, and returns once all have finished. Split out of
// BulkContainerAction so the concurrency bound itself can be unit-tested
// without a Docker daemon.
func boundedRun(n, concurrency int, fn func(i int)) {
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}
