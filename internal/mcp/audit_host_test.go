package mcp

import (
	"context"
	"errors"
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
	entries, err := st.RecentAudit(ctx, 10, 0, nil, true)
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

// mcpFixture: a local host, two remote ones, and an MCP server over real HTTP.
func mcpFixture(t *testing.T) (st *store.Store, url string, hostA, hostB int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	hostA, _ = st.CreateHost(ctx, &store.Host{Name: "a", Kind: "tcp", Address: "tcp://127.0.0.1:1"})
	hostB, _ = st.CreateHost(ctx, &store.Host{Name: "b", Kind: "tcp", Address: "tcp://127.0.0.1:2"})
	// Access as the server checks it: the effective grants, roles included.
	check := func(ctx context.Context, u *store.User, section string, write bool, hostID int64) error {
		if u.IsAdmin() {
			return nil
		}
		g, err := st.EffectiveGrants(ctx, u)
		if err != nil || !g[section].Granted {
			return errors.New("section not permitted")
		}
		if write && !g[section].Write {
			return errors.New("read-only")
		}
		if !g[section].HasHost(st.NormalizeHostID(ctx, hostID)) {
			return errors.New("host not permitted")
		}
		return nil
	}
	deps := Deps{Store: st, Docker: docker.NewManager(st), CheckAccess: check, Version: "test"}
	h, _ := deps.Handlers()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return st, ts.URL, hostA, hostB
}

// A tool aimed at a remote host that fails before reaching Docker (here: a
// duplicated container id) is still audited under that host, not as hostless.
func TestMCPPreDockerFailureIsAuditedUnderItsHost(t *testing.T) {
	st, url, _, hostB := mcpFixture(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, &store.User{Username: "op", PasswordHash: "x", Role: "user", Sections: []string{"hosts", "containers"}})
	if err != nil {
		t.Fatal(err)
	}
	mkToken(t, st, uid, "op-token-secret", nil, false)
	cs, cctx := connect(t, url, "op-token-secret")
	res, err := cs.CallTool(cctx, &mcpsdk.CallToolParams{Name: "stop_stack_containers", Arguments: map[string]any{
		"project": "app", "container_ids": []string{"dup", "dup"}, "host_id": hostB,
	}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a duplicated container id should be refused")
	}
	entries, err := st.RecentAudit(ctx, 10, 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "mcp.stack.stop" {
			if e.HostID != hostB {
				t.Fatalf("SECURITY: the refused call on host B was recorded under host %d", e.HostID)
			}
			return
		}
	}
	t.Fatalf("no mcp.stack.stop entry in %+v", entries)
}

// PENTEST: recent_audit returns only the hosts the principal may see. It used to
// return every host's entries to any holder of "audit".
func TestPentestMCPRecentAuditIsHostScoped(t *testing.T) {
	st, url, hostA, hostB := mcpFixture(t)
	ctx := context.Background()
	local, _ := st.LocalHostID(ctx)
	for target, host := range map[string]int64{"on-a": hostA, "on-b": hostB, "on-local": local, "hostless": 0, "several": store.AuditHostSeveral} {
		if err := st.Audit(ctx, store.AuditEntry{Action: "container.stop", Target: target, HostID: host}); err != nil {
			t.Fatal(err)
		}
	}
	uid, err := st.CreateUser(ctx, &store.User{Username: "auditor", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	roleID, err := st.CreateRole(ctx, &store.Role{Name: "Audit on A", Sections: []store.RoleSection{{Section: "audit"}}, HostIDs: []int64{hostA}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRoles(ctx, uid, []int64{roleID}); err != nil {
		t.Fatal(err)
	}
	mkToken(t, st, uid, "auditor-token-secret", nil, true)
	cs, cctx := connect(t, url, "auditor-token-secret")
	res, err := cs.CallTool(cctx, &mcpsdk.CallToolParams{Name: "recent_audit", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("recent_audit: %v %+v", err, res)
	}
	out := res.StructuredContent.(map[string]any)
	seen := map[string]bool{}
	for _, e := range out["entries"].([]any) {
		seen[e.(map[string]any)["target"].(string)] = true
	}
	for _, leaked := range []string{"on-b", "several"} {
		if seen[leaked] {
			t.Errorf("SECURITY: an auditor limited to host A read %q over MCP", leaked)
		}
	}
	for _, want := range []string{"on-a", "on-local", "hostless"} {
		if !seen[want] {
			t.Errorf("the auditor should see %q", want)
		}
	}
}
