package docker

import (
	"context"
	"fmt"
	"strings"
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

// FirstDuplicateID returns the first id in ids that also appears earlier in
// ids, or "" if every id is unique. Exported so MCP-layer callers (see
// internal/mcp/tools_parity.go) can refuse a duplicate-containing batch
// before spending their own per-container rate-limit budget on it, while
// BulkContainerAction below applies the same check as its own fail-closed
// guard for every caller — MCP or the REST bulk endpoint
// (internal/api/docker_handlers.go).
func FirstDuplicateID(ids []string) string {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return id
		}
		seen[id] = true
	}
	return ""
}

// BulkContainerAction runs action (restart|stop — callers must validate the
// action before calling this; ContainerAction itself rejects anything else)
// against each of ids, bounded to bulkActionConcurrency concurrent daemon
// calls. One container failing does not stop the others: every id gets a
// result, success or error, so the caller can report a per-container summary
// instead of a single aggregate ok/fail.
//
// Rejects a batch containing the same id twice, naming the duplicate, before
// running anything: one goroutine is started per INDEX, not per unique id, so
// [id, id] would otherwise fire two concurrent actions at the same container
// — a race, plus a doubled audit entry, for no legitimate request shape.
// Matches this package's fail-closed style elsewhere (the stack-membership
// check below): refuse the whole call, name the offender, execute nothing.
func (m *Manager) BulkContainerAction(ctx context.Context, hostID int64, ids []string, action string) ([]BulkActionResult, error) {
	if dup := FirstDuplicateID(ids); dup != "" {
		return nil, fmt.Errorf("container %q is listed more than once in the same request", dup)
	}
	results := make([]BulkActionResult, len(ids))
	boundedRun(len(ids), bulkActionConcurrency, func(i int) {
		id := ids[i]
		if err := m.ContainerAction(ctx, hostID, id, action); err != nil {
			results[i] = BulkActionResult{ID: id, Error: err.Error()}
			return
		}
		results[i] = BulkActionResult{ID: id, OK: true}
	})
	return results, nil
}

// BulkStackContainerAction restarts or stops a caller-chosen subset of one
// Compose stack's own containers. It is the middle ground between
// ContainerAction (one container) and StackAction (every container in the
// stack, unbounded): every id in ids must belong to project — verified here
// against the stack's `com.docker.compose.project` label, not trusted from
// the caller — or the WHOLE call is refused, naming the first id that
// doesn't belong. A partial match never partially executes; that would let a
// caller reach one container it never actually named the stack for.
//
// Scoping to a project the caller already named keeps this no wider in spirit
// than restart_stack/stop_stack: those act on every container of a stack the
// caller identified by name, this acts on a bounded slice of the same set.
// Restricting action to restart/stop (never start, pause, kill, ...) matches
// the same restriction the MCP tools built on this apply.
//
// On success it delegates to BulkContainerAction for the actual
// bounded-parallel execution — this function only adds the membership check.
func (m *Manager) BulkStackContainerAction(ctx context.Context, hostID int64, project string, ids []string, action string) ([]BulkActionResult, error) {
	switch action {
	case "restart", "stop":
	default:
		return nil, ErrUnknownAction
	}
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("project is required")
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("ids is required")
	}

	containers, err := m.ListContainers(ctx, hostID)
	if err != nil {
		return nil, err
	}
	member := make(map[string]bool, len(containers))
	for _, c := range containers {
		if c.Labels[labelComposeProject] == project {
			member[c.ID] = true
		}
	}
	// Fail closed: check every id before running any of them, so a caller
	// cannot use one valid id to smuggle a second, unrelated one through in
	// the same call.
	for _, id := range ids {
		if !member[id] {
			return nil, fmt.Errorf("container %q does not belong to stack %q", id, project)
		}
	}
	// BulkContainerAction applies its own duplicate check before running
	// anything, so [id, id] is refused here too even though membership alone
	// would have passed it.
	return m.BulkContainerAction(ctx, hostID, ids, action)
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
