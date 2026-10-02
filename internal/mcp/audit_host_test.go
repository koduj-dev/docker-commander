package mcp

import (
	"context"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// A tool call through the real HTTP transport is audited under the Docker host
// it reached. This proves the request's recorder reaches the tool handler (the
// SDK passes the HTTP request's context on) and that Client fills it in: the
// call names no host, so it lands on the local daemon, which must be recorded
// by the local host's own id rather than 0, "no host".
//
// The container doesn't exist, so the action fails; a failed control action is
// audited too, which is all this needs. Creating the client contacts no daemon.
func TestMCPToolCallIsAuditedUnderTheHostItReached(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	local, err := st.LocalHostID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(ctx, &store.User{Username: "smoke", PasswordHash: "x", Role: "user", Sections: []string{"hosts", "containers"}})
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: st, Docker: docker.NewManager(st), CheckAccess: smokeCheckAccess, Version: "test"}
	h, _ := deps.Handlers()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	mkToken(t, st, uid, "rw-token-secret", nil, false)
	cs, cctx := connect(t, ts.URL, "rw-token-secret")

	if _, err := cs.CallTool(cctx, &mcpsdk.CallToolParams{
		Name: "start_container", Arguments: map[string]any{"container_id": "dctest-no-such-container"},
	}); err != nil {
		t.Fatalf("call: %v", err)
	}
	entries, err := st.RecentAudit(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "mcp.container.start" {
			if e.HostID != local {
				t.Fatalf("mcp.container.start recorded under host %d, want the local host %d", e.HostID, local)
			}
			return
		}
	}
	t.Fatalf("no mcp.container.start entry in %+v", entries)
}
