package mcp

import (
	"context"
	"errors"
	"testing"
)

// The stack tools claim the project that owns the stack before they touch
// Docker, so a stack action can't stop or restart a project's containers in the
// middle of its deploy or restore. The handler has no Docker here: reaching it
// would panic, which is the point.
func TestStackToolsRefuseWhenTheProjectIsBusy(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	busy := errors.New("this project is busy (a restore is running)")
	var asked []string
	h.deps.BeginStackOp = func(_ context.Context, _ int64, stack, what string) (func(), error) {
		asked = append(asked, stack+"/"+what)
		return nil, busy
	}
	u, err := h.deps.Store.UserByID(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	if _, _, err := h.stackActionTool("restart")(context.Background(), req, stackActionInput{Project: "shop"}); !errors.Is(err, busy) {
		t.Errorf("restart_stack: %v, want the busy refusal", err)
	}
	if _, _, err := h.stackContainersActionTool("stop")(context.Background(), req, stackContainersActionInput{Project: "shop", ContainerIDs: []string{"c1"}}); !errors.Is(err, busy) {
		t.Errorf("stop_stack_containers: %v, want the busy refusal", err)
	}
	if len(asked) != 2 || asked[0] != "shop/a stack restart" || asked[1] != "shop/a stack stop" {
		t.Errorf("claims asked for: %q", asked)
	}
}
