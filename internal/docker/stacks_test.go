package docker

import (
	"context"
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
