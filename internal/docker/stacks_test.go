package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
)

// createLabeled starts an alpine container with the given Compose labels via
// the raw client (CreateSpec doesn't expose labels).
func createLabeled(ctx context.Context, t *testing.T, m *Manager, name string, labels map[string]string) string {
	t.Helper()
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	freeName(ctx, m, name)
	created, err := cli.ContainerCreate(ctx,
		&container.Config{Image: testImage, Cmd: []string{"sleep", "300"}, Labels: labels},
		&container.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() { rmContainer(ctx, t, m, created.ID) })
	return created.ID
}

func TestIntegrationStacks(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	const project = "dctest_stack"
	createLabeled(ctx, t, m, "dctest_stack_web", map[string]string{
		labelComposeProject: project, labelComposeService: "web",
	})
	createLabeled(ctx, t, m, "dctest_stack_db", map[string]string{
		labelComposeProject: project, labelComposeService: "db",
	})

	find := func() *Stack {
		stacks, err := m.ListStacks(ctx, 0)
		if err != nil {
			t.Fatalf("ListStacks: %v", err)
		}
		for i := range stacks {
			if stacks[i].Project == project {
				return &stacks[i]
			}
		}
		return nil
	}

	st := find()
	if st == nil || len(st.Containers) != 2 || st.Running != 2 {
		t.Fatalf("expected a 2-container running stack, got %+v", st)
	}
	if st.Containers[0].Service != "db" || st.Containers[1].Service != "web" {
		t.Errorf("services should be sorted (db, web): %+v", st.Containers)
	}

	// StackContainerIDs must match the same label filter StackAction uses
	// internally — it exists so a caller (the MCP layer) can size a
	// rate-limit charge to the stack's real container count before StackAction
	// itself runs.
	ids, err := m.StackContainerIDs(ctx, 0, project)
	if err != nil {
		t.Fatalf("StackContainerIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("StackContainerIDs: got %d ids, want 2: %v", len(ids), ids)
	}
	wantIDs := map[string]bool{st.Containers[0].ID: true, st.Containers[1].ID: true}
	for _, id := range ids {
		if !wantIDs[id] {
			t.Errorf("StackContainerIDs returned %q, not one of the stack's own containers", id)
		}
	}

	// Stop the whole stack.
	if err := m.StackAction(ctx, 0, project, "stop"); err != nil {
		t.Fatalf("StackAction stop: %v", err)
	}
	// Poll rather than assert instantly. Engine 24 (API 1.43) serves a briefly
	// STALE /containers/json after a stop: inspect reports "exited" immediately
	// while the list still says "running" for ~250ms. Newer engines are
	// consistent at once. The app polls this endpoint, so it self-corrects within
	// one refresh — asserting instantaneously is stricter than the app needs, and
	// it is what made this test the only failure in the Engine 24 matrix run.
	if st := findStopped(t, find); st == nil || st.Running != 0 {
		t.Errorf("stack should be stopped: %+v", st)
	}

	// Unknown action is rejected.
	if err := m.StackAction(ctx, 0, project, "bogus"); err != ErrUnknownAction {
		t.Errorf("unknown stack action should be ErrUnknownAction, got %v", err)
	}

	// Remove the stack — it disappears from the list.
	if err := m.StackAction(ctx, 0, project, "remove"); err != nil {
		t.Fatalf("StackAction remove: %v", err)
	}
	if st := find(); st != nil {
		t.Errorf("stack should be gone after remove: %+v", st)
	}
}

// TestStackActionOnIDsUnaffectedByLateArrivals is the FUP-SEC-002 regression:
// a container that joins the project AFTER StackContainerIDs already
// resolved the set to act on must not be touched — StackActionOnIDs acts on
// exactly the ids it's given, not on whatever ListContainers would return if
// asked again. This is what keeps an MCP rate-limit charge (sized from the
// first resolution) honest against what actually runs: StackAction, by
// contrast, re-resolves membership internally and WOULD pick up the late
// arrival — see TestIntegrationStacks's plain StackAction calls for that
// (intentionally different) behavior.
func TestStackActionOnIDsUnaffectedByLateArrivals(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(ctx, t, m)

	const project = "dctest_stack_late"
	idA := createLabeled(ctx, t, m, "dctest_stack_late_a", map[string]string{
		labelComposeProject: project, labelComposeService: "a",
	})
	idB := createLabeled(ctx, t, m, "dctest_stack_late_b", map[string]string{
		labelComposeProject: project, labelComposeService: "b",
	})

	// The snapshot an MCP call would resolve and charge for — taken BEFORE
	// the third container joins, simulating a concurrent deploy landing in
	// the window between "resolve ids" and "act on them".
	ids, err := m.StackContainerIDs(ctx, 0, project)
	if err != nil {
		t.Fatalf("StackContainerIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("StackContainerIDs: got %d ids before the late arrival, want 2: %v", len(ids), ids)
	}

	idLate := createLabeled(ctx, t, m, "dctest_stack_late_c", map[string]string{
		labelComposeProject: project, labelComposeService: "c",
	})

	if err := m.StackActionOnIDs(ctx, 0, ids, "stop"); err != nil {
		t.Fatalf("StackActionOnIDs: %v", err)
	}

	running := func(id string) bool {
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
	// Poll: the same brief Engine-24 list-vs-inspect staleness
	// TestIntegrationStacks works around applies here too.
	deadline := time.Now().Add(2 * time.Second)
	for running(idA) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if running(idA) {
		t.Errorf("idA (in the resolved snapshot) should have been stopped")
	}
	if running(idB) {
		t.Errorf("idB (in the resolved snapshot) should have been stopped")
	}
	if !running(idLate) {
		t.Errorf("SECURITY/correctness: idLate joined the project AFTER the snapshot was resolved and must " +
			"not have been touched by an action sized/charged against that earlier snapshot, but it was stopped anyway")
	}
}

// TestRunningImageDigest_RealContainer exercises the real container→image→
// RepoDigests chain against a genuinely pulled image, since digest_test.go's
// coverage of ResolveImageDigest is all fake-registry unit tests — this is the
// half of AugmentDigestDrift's plumbing that actually talks to the daemon.
func TestRunningImageDigest_RealContainer(t *testing.T) {
	m, ctx := newManager(t)
	id := startTestContainer(ctx, t, m, "dctest_digest")

	digest, err := m.RunningImageDigest(ctx, 0, id, testImage)
	if err != nil {
		t.Fatalf("RunningImageDigest: %v", err)
	}
	if digest == "" {
		t.Skip("local alpine:latest has no RepoDigests (not pulled from a registry in this environment) — nothing to assert")
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest = %q, want a sha256: prefix", digest)
	}
}

// findStopped waits for the container list to agree that nothing is running,
// bounded so a genuine failure to stop still fails the test promptly.
func findStopped(t *testing.T, find func() *Stack) *Stack {
	t.Helper()
	var st *Stack
	for i := 0; i < 40; i++ {
		st = find()
		if st == nil || st.Running == 0 {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	return st
}
