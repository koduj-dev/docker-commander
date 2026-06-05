package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// createLabeled starts an alpine container with the given Compose labels via
// the raw client (CreateSpec doesn't expose labels).
func createLabeled(t *testing.T, m *Manager, ctx context.Context, name string, labels map[string]string) string {
	t.Helper()
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	created, err := cli.ContainerCreate(ctx,
		&container.Config{Image: testImage, Cmd: []string{"sleep", "300"}, Labels: labels},
		&container.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() { rmContainer(t, m, ctx, created.ID) })
	return created.ID
}

func TestIntegrationStacks(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(t, m, ctx)

	const project = "dctest_stack"
	createLabeled(t, m, ctx, "dctest_stack_web", map[string]string{
		labelComposeProject: project, labelComposeService: "web",
	})
	createLabeled(t, m, ctx, "dctest_stack_db", map[string]string{
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

	// Stop the whole stack.
	if err := m.StackAction(ctx, 0, project, "stop"); err != nil {
		t.Fatalf("StackAction stop: %v", err)
	}
	if st := find(); st == nil || st.Running != 0 {
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
