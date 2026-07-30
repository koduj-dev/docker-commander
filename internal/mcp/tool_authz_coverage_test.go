package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Systemic MCP authorization coverage: the analogue of the REST route-coverage
// test. The per-tool tests each check a tool someone remembered to check; what
// they cannot catch is a NEW tool that simply never calls authorize(). Such a tool
// would run with no RBAC at all and every existing test would still pass.
//
// So: enumerate every tool the server actually advertises, call each one with a
// gate that DENIES everything, and require each to fail with that gate's sentinel.
// A tool that skips authorize() cannot produce the sentinel and is reported.

// denySentinel is distinctive enough that it can only come from the gate below.
const denySentinel = "DENIED-BY-COVERAGE-GATE"

func denyAllCheckAccess(_ context.Context, _ *store.User, _ string, _ bool, _ int64) error {
	return errors.New(denySentinel)
}

// newDenyAllServer is newSmokeServer with the gate replaced by a deny-all one.
// The user is an ordinary account: admins bypass RBAC by design, which would make
// this test vacuous.
func newDenyAllServer(t *testing.T) (*httptest.Server, *store.Store, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateHost(context.Background(), &store.Host{Name: "local", Kind: "local"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	uid, err := st.CreateUser(context.Background(), &store.User{
		Username: "denied", PasswordHash: "x", Role: "user",
		// Deliberately broad: the account's own grants must not matter, because the
		// gate refuses everything. If a tool passes, it never consulted the gate.
		Sections: store.Sections,
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	deps := Deps{
		Store: st, CheckAccess: denyAllCheckAccess, Version: "test",
		ResourceURL: "https://cov.test/mcp",
		MetadataURL: "https://cov.test/.well-known/oauth-protected-resource",
		IssuerURL:   "https://cov.test",
	}
	h, _ := deps.Handlers()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, st, uid
}

// TestEveryToolConsultsTheAccessGate walks the advertised tool list and asserts
// each one is refused when the gate denies everything.
func TestEveryToolConsultsTheAccessGate(t *testing.T) {
	ts, st, uid := newDenyAllServer(t)
	mkToken(t, st, uid, "cov-secret", nil, false)
	cs, ctx := connect(t, ts.URL, "cov-secret")

	tools, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools advertised — the harness is not exercising anything")
	}

	var unguarded []string
	for _, tool := range tools.Tools {
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: tool.Name, Arguments: argsFromSchema(tool.InputSchema),
		})
		// The refusal may arrive as a protocol error or as an IsError result;
		// either is fine, so long as it carries the gate's sentinel.
		text := ""
		if err != nil {
			text = err.Error()
		}
		if res != nil {
			if !res.IsError && err == nil {
				// Succeeded outright with every section denied.
				unguarded = append(unguarded, tool.Name+" (succeeded)")
				continue
			}
			for _, c := range res.Content {
				if tc, ok := c.(*mcpsdk.TextContent); ok {
					text += " " + tc.Text
				}
			}
		}
		if !strings.Contains(text, denySentinel) {
			unguarded = append(unguarded, tool.Name+" (refused, but not by the gate: "+trim(text)+")")
		}
	}
	if len(unguarded) > 0 {
		t.Errorf(`SECURITY: %d of %d MCP tool(s) did not consult the access gate.
A tool that never calls authorize() runs with no RBAC at all:
  %s`, len(unguarded), len(tools.Tools), strings.Join(unguarded, "\n  "))
	}
}

// A tool must also be refused when the gate ALLOWS but the token's own scope does
// not — narrowing runs before the gate, and this proves the ordering holds for
// every advertised tool rather than for the two the smoke tests happen to use.
func TestEveryToolRespectsTokenScope(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx0 := context.Background()
	if _, err := st.CreateHost(ctx0, &store.Host{Name: "local", Kind: "local"}); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(ctx0, &store.User{
		Username: "scoped", PasswordHash: "x", Role: "user", Sections: store.Sections,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Gate allows everything; only the token scope restricts.
	deps := Deps{
		Store: st,
		CheckAccess: func(context.Context, *store.User, string, bool, int64) error {
			return nil
		},
		Version: "test", ResourceURL: "https://cov.test/mcp",
		MetadataURL: "https://cov.test/.well-known/oauth-protected-resource",
		IssuerURL:   "https://cov.test",
	}
	h, _ := deps.Handlers()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	// A scope naming a section no tool uses, so every tool must be out of scope.
	mkToken(t, st, uid, "scoped-secret", []string{"registries"}, false)
	cs, ctx := connect(t, ts.URL, "scoped-secret")

	tools, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var leaked []string
	for _, tool := range tools.Tools {
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: tool.Name, Arguments: argsFromSchema(tool.InputSchema),
		})
		text := ""
		if err != nil {
			text = err.Error()
		}
		if res != nil {
			for _, c := range res.Content {
				if tc, ok := c.(*mcpsdk.TextContent); ok {
					text += " " + tc.Text
				}
			}
			if !res.IsError && err == nil {
				leaked = append(leaked, tool.Name+" (succeeded outside its scope)")
				continue
			}
		}
		// "not scoped" is narrowed()'s wording; anything else means the refusal came
		// from somewhere other than the scope check.
		if !strings.Contains(text, "not scoped") {
			leaked = append(leaked, tool.Name+" (refused for another reason: "+trim(text)+")")
		}
	}
	if len(leaked) > 0 {
		t.Errorf(`SECURITY: %d of %d MCP tool(s) ignored the token's section scope:
  %s`, len(leaked), len(tools.Tools), strings.Join(leaked, "\n  "))
	}
}

// argsFromSchema synthesises a minimally-valid argument set from a tool's declared
// input schema, so the SDK's own JSON-schema validation passes and the handler
// actually runs. Without this the request is rejected before the handler is
// entered, which looks exactly like "the tool didn't authorize" — the first
// version of this test drew precisely that wrong conclusion.
func argsFromSchema(schema any) map[string]any {
	out := map[string]any{}
	obj, ok := schema.(map[string]any)
	if !ok {
		return out
	}
	required, _ := obj["required"].([]any)
	props, _ := obj["properties"].(map[string]any)
	for _, r := range required {
		name, ok := r.(string)
		if !ok {
			continue
		}
		kind := "string"
		if props != nil {
			if p, ok := props[name].(map[string]any); ok {
				if tn, ok := p["type"].(string); ok {
					kind = tn
				}
			}
		}
		switch kind {
		case "integer", "number":
			out[name] = 1
		case "boolean":
			out[name] = true
		case "array":
			out[name] = []any{}
		case "object":
			out[name] = map[string]any{}
		default:
			out[name] = "x"
		}
	}
	return out
}

func trim(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
