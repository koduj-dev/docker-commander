package docker

import (
	"context"
	"strings"
	"testing"
)

// TestBulkStackContainerActionRejectsUnknownAction guards the action
// allowlist — restart/stop only, matching the same restriction
// containerActionTool/stackActionTool already apply. This check happens
// before any Docker call, so it runs with a bare Manager (no store, no
// daemon) and stays fast under -short.
func TestBulkStackContainerActionRejectsUnknownAction(t *testing.T) {
	m := &Manager{}
	ctx := context.Background()
	for _, action := range []string{"start", "pause", "unpause", "kill", "remove", "bogus", ""} {
		if _, err := m.BulkStackContainerAction(ctx, 0, "web", []string{"x"}, action); err != ErrUnknownAction {
			t.Errorf("action %q: want ErrUnknownAction, got %v", action, err)
		}
	}
}

// TestBulkStackContainerActionRejectsEmptyInput guards project/ids before any
// Docker call, same fast-path rationale as above.
func TestBulkStackContainerActionRejectsEmptyInput(t *testing.T) {
	m := &Manager{}
	ctx := context.Background()
	if _, err := m.BulkStackContainerAction(ctx, 0, "   ", []string{"x"}, "stop"); err == nil {
		t.Error("an empty (or blank) project must be refused")
	}
	if _, err := m.BulkStackContainerAction(ctx, 0, "web", nil, "stop"); err == nil {
		t.Error("no ids must be refused")
	}
}

// containerIsRunning reports whether id is currently running, by consulting
// the live container list (not a cached inspect) — the same source
// BulkStackContainerAction itself reads membership from.
func containerIsRunning(ctx context.Context, t *testing.T, m *Manager, id string) bool {
	t.Helper()
	cs, err := m.ListContainers(ctx, 0)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	for _, c := range cs {
		if c.ID == id {
			return c.State == "running"
		}
	}
	return false
}

// TestIntegrationBulkStackContainerAction proves the happy path: a
// caller-chosen SUBSET of a stack's containers, all verified members, is
// delegated to BulkContainerAction for the real work — a container that
// belongs to the SAME stack but was not named in the call is left alone.
func TestIntegrationBulkStackContainerAction(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	const project = "dctest_bulkstack"
	idA := createLabeled(ctx, t, m, "dctest_bulkstack_a", map[string]string{
		labelComposeProject: project, labelComposeService: "a",
	})
	idB := createLabeled(ctx, t, m, "dctest_bulkstack_b", map[string]string{
		labelComposeProject: project, labelComposeService: "b",
	})
	idC := createLabeled(ctx, t, m, "dctest_bulkstack_c", map[string]string{
		labelComposeProject: project, labelComposeService: "c",
	})

	results, err := m.BulkStackContainerAction(ctx, 0, project, []string{idA, idB}, "restart")
	if err != nil {
		t.Fatalf("BulkStackContainerAction: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results for 2 requested ids, got %d: %+v", len(results), results)
	}
	byID := map[string]BulkActionResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	for _, id := range []string{idA, idB} {
		if r, ok := byID[id]; !ok || !r.OK {
			t.Errorf("container %s should have succeeded: %+v", id, r)
		}
	}
	if _, touched := byID[idC]; touched {
		t.Errorf("idC was not named in the call and must not appear in the results: %+v", results)
	}
}

// TestPentestBulkStackContainerActionRefusesCrossStackContainer.
//
// A container id that genuinely exists — just not in the stack the caller
// named — must refuse the WHOLE call, not silently drop it and not act on it.
// This is exactly the property that makes exposing "act on some of a stack's
// containers" over MCP safe: a caller can never reach a container outside the
// project it already named for itself.
func TestPentestBulkStackContainerActionRefusesCrossStackContainer(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	const projectA = "dctest_bulkstack_crossa"
	const projectB = "dctest_bulkstack_crossb"
	idA := createLabeled(ctx, t, m, "dctest_bulkstack_cross_a", map[string]string{
		labelComposeProject: projectA, labelComposeService: "a",
	})
	idB := createLabeled(ctx, t, m, "dctest_bulkstack_cross_b", map[string]string{
		labelComposeProject: projectB, labelComposeService: "b",
	})

	// stop, not restart: a running container that WAS wrongly acted on turns
	// "exited" — an observable side effect the assertions below check for.
	results, err := m.BulkStackContainerAction(ctx, 0, projectA, []string{idA, idB}, "stop")
	if err == nil {
		t.Fatalf("SECURITY: a container from stack %q was accepted while acting on stack %q; results=%+v",
			projectB, projectA, results)
	}
	if !strings.Contains(err.Error(), idB) {
		t.Errorf("the refusal should name the offending id %q, got: %v", idB, err)
	}
	if results != nil {
		t.Errorf("a refused call must return no results, got %+v", results)
	}

	// Fail CLOSED, whole call: idA legitimately belongs to projectA, but must
	// not have been stopped just because idB was smuggled into the same
	// request. A partial match must not partially execute.
	if !containerIsRunning(ctx, t, m, idA) {
		t.Error("SECURITY: idA (a legitimate member of the named stack) was acted on despite the call being refused " +
			"— the whole call must fail closed, not run everything except the offending id")
	}
}

// TestPentestBulkStackContainerActionRefusesUnlabeledContainer.
//
// A container carrying no Compose project label at all (never deployed by
// `docker compose`, or a stray one-off container) must be refused just like
// one from a different stack — "not a member of this project" covers both,
// there is no special case for "belongs to nothing".
func TestPentestBulkStackContainerActionRefusesUnlabeledContainer(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	const project = "dctest_bulkstack_unlabeled"
	idMember := createLabeled(ctx, t, m, "dctest_bulkstack_unlabeled_member", map[string]string{
		labelComposeProject: project, labelComposeService: "a",
	})
	idStray := startTestContainer(ctx, t, m, "dctest_bulkstack_unlabeled_stray")

	results, err := m.BulkStackContainerAction(ctx, 0, project, []string{idMember, idStray}, "restart")
	if err == nil {
		t.Fatalf("SECURITY: an unlabeled container was accepted into a stack-scoped bulk action; results=%+v", results)
	}
	if !strings.Contains(err.Error(), idStray) {
		t.Errorf("the refusal should name the offending id %q, got: %v", idStray, err)
	}
}
