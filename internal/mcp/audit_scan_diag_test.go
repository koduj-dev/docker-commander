package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// run_diagnostics is a write (it runs commands on remote hosts over SSH) and the
// REST run is audited; the MCP run must be too, under the host it ran on.
func TestMCPRunDiagnosticsIsAudited(t *testing.T) {
	st, url, _, _ := mcpFixture(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, &store.User{Username: "diag", PasswordHash: "x", Role: "user", Sections: []string{"diagnostics"}})
	if err != nil {
		t.Fatal(err)
	}
	mkToken(t, st, uid, "diag-token-secret", nil, false)
	cs, cctx := connect(t, url, "diag-token-secret")
	// No daemon is needed: a failed run is audited too, which is all this checks.
	if _, err := cs.CallTool(cctx, &mcpsdk.CallToolParams{Name: "run_diagnostics", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("call: %v", err)
	}
	local, _ := st.LocalHostID(ctx)
	entries, err := st.RecentAudit(ctx, 10, 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "mcp.diagnostics.run" {
			if e.HostID != local {
				t.Errorf("recorded under host %d, want the local host %d", e.HostID, local)
			}
			return
		}
	}
	t.Fatalf("run_diagnostics over MCP left no audit entry: %+v", entries)
}
