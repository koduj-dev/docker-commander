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

// Systemic per-HOST authorization coverage for tools that take a project id.
//
// TestEveryToolConsultsTheAccessGate proves each tool calls authorize(). It
// cannot prove the tool authorizes against the RIGHT host, and that distinction
// is not academic: a project names its target host in its own record rather than
// in the tool arguments, so a tool that authorizes "projects" against host 0 and
// then acts on the project passes the deny-all sweep while still reaching a host
// the caller may not touch. `preview_deploy` shipped with exactly that hole.
//
// So this walks the advertised tools, picks out every one accepting a
// `project_id`, and requires each to refuse a project on an out-of-scope host —
// and, more strongly, requires the underlying operation never to run. A new
// project tool is covered the moment it is advertised; nobody has to remember to
// add it here.

const scopeSentinel = "DENIED-OUT-OF-SCOPE-HOST"

// newHostScopedServer allows the local daemon and host `inScope`, and refuses
// every other host with a recognisable sentinel.
func newHostScopedServer(t *testing.T, inScope int64) (*httptest.Server, *store.Store, int64, *[]string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.CreateHost(ctx, &store.Host{Name: "local", Kind: "local"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	uid, err := st.CreateUser(ctx, &store.User{
		Username: "scoped", PasswordHash: "x", Role: "user", Sections: store.Sections,
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}

	// Records every project operation that actually ran. Anything appearing here
	// for an out-of-scope project is a breach, not merely a missing error.
	reached := &[]string{}

	deps := Deps{
		Store: st, Version: "test",
		CheckAccess: func(_ context.Context, _ *store.User, _ string, _ bool, hostID int64) error {
			if hostID == 0 || hostID == inScope {
				return nil
			}
			return errors.New(scopeSentinel)
		},
		// Wired so a refusal cannot come from "this is not available on this
		// server", which would make the whole sweep vacuous.
		ListProjects: func(context.Context) ([]ManagedProject, error) {
			var out []ManagedProject
			projs, err := st.ListProjects(ctx)
			if err != nil {
				return nil, err
			}
			for _, p := range projs {
				out = append(out, ManagedProject{ID: p.ID, Name: p.Name, Slug: p.Slug, HostID: p.HostID, Deployed: true})
			}
			return out, nil
		},
		DeployProject: func(_ context.Context, id int64, _ []string) (string, error) {
			*reached = append(*reached, "deploy")
			return "deployed", nil
		},
		DownProject: func(_ context.Context, id int64) (string, error) {
			*reached = append(*reached, "down")
			return "downed", nil
		},
		PreviewProject: func(_ context.Context, id int64) (ProjectPreview, error) {
			*reached = append(*reached, "preview")
			return ProjectPreview{Valid: true, Project: "secret"}, nil
		},
		ResourceURL: "https://scope.test/mcp",
		MetadataURL: "https://scope.test/.well-known/oauth-protected-resource",
		IssuerURL:   "https://scope.test",
	}
	h, _ := deps.Handlers()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, st, uid, reached
}

// takesProjectID reports whether a tool's input schema has a project_id field.
func takesProjectID(schema any) bool {
	obj, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	props, _ := obj["properties"].(map[string]any)
	_, has := props["project_id"]
	return has
}

func TestEveryProjectToolChecksTheProjectsHost(t *testing.T) {
	const inScope, outOfScope = int64(7), int64(8)
	ts, st, uid, reached := newHostScopedServer(t, inScope)
	ctx0 := context.Background()

	for _, h := range []struct {
		name string
		id   int64
	}{{"good", inScope}, {"bad", outOfScope}} {
		if _, err := st.CreateHost(ctx0, &store.Host{Name: h.name, Kind: "tcp", Address: "tcp://127.0.0.1:1"}); err != nil {
			t.Fatalf("seed host: %v", err)
		}
	}
	okProject, err := st.CreateProject(ctx0, &store.Project{Name: "ok", Slug: "ok", ComposeFile: "compose.yml", HostID: inScope})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	badProject, err := st.CreateProject(ctx0, &store.Project{Name: "hidden", Slug: "hidden", ComposeFile: "compose.yml", HostID: outOfScope})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	mkToken(t, st, uid, "scope-secret", nil, false)
	cs, ctx := connect(t, ts.URL, "scope-secret")

	tools, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	call := func(name string, id int64) (string, bool) {
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: name, Arguments: map[string]any{"project_id": id},
		})
		text := ""
		failed := err != nil
		if err != nil {
			text = err.Error()
		}
		if res != nil {
			if res.IsError {
				failed = true
			}
			for _, c := range res.Content {
				if tc, ok := c.(*mcpsdk.TextContent); ok {
					text += " " + tc.Text
				}
			}
		}
		return text, failed
	}

	var checked []string
	var leaky []string
	for _, tool := range tools.Tools {
		if !takesProjectID(tool.InputSchema) {
			continue
		}
		checked = append(checked, tool.Name)

		*reached = (*reached)[:0]
		text, failed := call(tool.Name, badProject)
		switch {
		case !failed:
			leaky = append(leaky, tool.Name+" (succeeded on an out-of-scope host)")
		case !strings.Contains(text, scopeSentinel):
			leaky = append(leaky, tool.Name+" (refused, but not by the host gate: "+trim(text)+")")
		case len(*reached) > 0:
			leaky = append(leaky, tool.Name+" (ran the operation anyway: "+strings.Join(*reached, ",")+")")
		}

		// The mirror: the same tool must still work for a project on a host the
		// caller CAN reach, or the gate is simply refusing everything and proves
		// nothing.
		*reached = (*reached)[:0]
		if _, failed := call(tool.Name, okProject); failed {
			t.Errorf("%s refused a project on an in-scope host", tool.Name)
		}
	}

	if len(checked) == 0 {
		t.Fatal("no advertised tool takes a project_id — the sweep is not exercising anything")
	}
	if len(leaky) > 0 {
		t.Errorf(`SECURITY: %d project tool(s) did not check the project's host:
  %s`, len(leaky), strings.Join(leaky, "\n  "))
	}
}

// The list must not disclose that an out-of-scope project exists at all. A tool
// that refuses to act on it but happily names it has still leaked the workload
// inventory of a host the caller cannot reach.
func TestListManagedProjectsHidesOutOfScopeHosts(t *testing.T) {
	const inScope, outOfScope = int64(7), int64(8)
	ts, st, uid, _ := newHostScopedServer(t, inScope)
	ctx0 := context.Background()

	for _, h := range []struct{ name string }{{"good"}, {"bad"}} {
		if _, err := st.CreateHost(ctx0, &store.Host{Name: h.name, Kind: "tcp", Address: "tcp://127.0.0.1:1"}); err != nil {
			t.Fatalf("seed host: %v", err)
		}
	}
	if _, err := st.CreateProject(ctx0, &store.Project{Name: "ok", Slug: "ok", ComposeFile: "compose.yml", HostID: inScope}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(ctx0, &store.Project{Name: "hidden", Slug: "hidden", ComposeFile: "compose.yml", HostID: outOfScope}); err != nil {
		t.Fatal(err)
	}

	mkToken(t, st, uid, "list-secret", nil, false)
	cs, ctx := connect(t, ts.URL, "list-secret")

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_managed_projects"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	body := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			body += tc.Text
		}
	}
	if strings.Contains(body, "hidden") {
		t.Errorf("SECURITY: a project on an out-of-scope host was listed:\n%s", body)
	}
	if !strings.Contains(body, "ok") {
		t.Errorf("the in-scope project vanished too, so the filter is not selective:\n%s", body)
	}
}
