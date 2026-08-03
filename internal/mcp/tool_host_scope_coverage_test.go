package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Systemic per-HOST authorization coverage for tools that resolve their host
// from a RECORD rather than from an argument.
//
// TestEveryToolConsultsTheAccessGate proves each tool calls authorize(). It
// cannot prove the tool authorizes against the RIGHT host, and that distinction
// is not academic: `preview_deploy`, `alert_delivery` and `acknowledge_alert` all
// shipped checking their section against host 0 while acting on a record
// belonging to another host. Every one of them passed the deny-all sweep.
//
// A tool is at risk of this precisely when it takes an integer id and NO
// host_id: the id implies a host that nothing in the arguments names. That
// property is machine-detectable, so the sweep below finds such tools itself and
// FAILS on any it has no fixture for — a new one cannot be added without someone
// deciding how it is host-scoped.

const scopeSentinel = "DENIED-OUT-OF-SCOPE-HOST"

// recordFixture describes how to exercise one record-scoped tool: the argument
// carrying the id, an id on a reachable host, one on an unreachable host, and a
// check that a refused call left the record alone.
type recordFixture struct {
	arg             string
	inScope         int64
	outOfScope      int64
	verifyUntouched func(t *testing.T, st *store.Store)
}

// newHostScopedServer allows the local daemon and host `inScope`, refusing every
// other host with a recognisable sentinel.
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

	// Records every project operation that actually ran. Anything here for an
	// out-of-scope record is a breach, not merely a missing error.
	reached := &[]string{}

	deps := Deps{
		Store: st, Version: "test",
		CheckAccess: func(_ context.Context, _ *store.User, _ string, _ bool, hostID int64) error {
			if hostID == 0 || hostID == inScope {
				return nil
			}
			return errors.New(scopeSentinel)
		},
		// Wired so a refusal cannot come from "not available on this server",
		// which would make the sweep vacuous.
		ListProjects: func(context.Context) ([]ManagedProject, error) {
			projs, err := st.ListProjects(ctx)
			if err != nil {
				return nil, err
			}
			var out []ManagedProject
			for _, p := range projs {
				out = append(out, ManagedProject{ID: p.ID, Name: p.Name, Slug: p.Slug, HostID: p.HostID, Deployed: true})
			}
			return out, nil
		},
		DeployProject: func(context.Context, int64, []string) (string, error) {
			*reached = append(*reached, "deploy")
			return "deployed", nil
		},
		DownProject: func(context.Context, int64) (string, error) {
			*reached = append(*reached, "down")
			return "downed", nil
		},
		PreviewProject: func(context.Context, int64) (ProjectPreview, error) {
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

// recordScopedArg reports the argument by which a tool identifies a record whose
// host is not named in the arguments: an integer `id` / `*_id` with no host_id
// alongside it. Returns "" when the tool is not of that shape.
func recordScopedArg(schema any) string {
	raw, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	var obj struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if _, named := obj.Properties["host_id"]; named {
		return "" // the host is an argument; the ordinary host_id checks apply
	}
	for name, p := range obj.Properties {
		if p.Type != "integer" {
			continue
		}
		if name == "id" || strings.HasSuffix(name, "_id") {
			return name
		}
	}
	return ""
}

func TestEveryRecordScopedToolChecksItsRecordsHost(t *testing.T) {
	const inScope, outOfScope = int64(7), int64(8)
	ts, st, uid, reached := newHostScopedServer(t, inScope)
	ctx0 := context.Background()

	for _, name := range []string{"good", "bad"} {
		if _, err := st.CreateHost(ctx0, &store.Host{Name: name, Kind: "tcp", Address: "tcp://127.0.0.1:1"}); err != nil {
			t.Fatalf("seed host: %v", err)
		}
	}
	mkProject := func(name string, hostID int64) int64 {
		id, err := st.CreateProject(ctx0, &store.Project{Name: name, Slug: name, ComposeFile: "compose.yml", HostID: hostID})
		if err != nil {
			t.Fatalf("seed project: %v", err)
		}
		return id
	}
	mkAlert := func(hostID int64) int64 {
		id, err := st.InsertAlertEvent(ctx0, &store.AlertEvent{
			RuleName: "r", Type: "resource", Severity: "warning", HostID: hostID, Message: "m",
		})
		if err != nil {
			t.Fatalf("seed alert: %v", err)
		}
		return id
	}

	okProject, badProject := mkProject("ok", inScope), mkProject("hidden", outOfScope)
	okAlert, badAlert := mkAlert(inScope), mkAlert(outOfScope)

	// A refused ack must leave the alert unacknowledged: an error that still
	// performed the write would otherwise pass.
	alertUntouched := func(t *testing.T, st *store.Store) {
		evs, _, err := st.ListAlertEvents(ctx0, store.AlertQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.ID == badAlert && e.Acknowledged {
				t.Error("the refused call acknowledged the out-of-scope alert anyway")
			}
		}
	}
	projectUntouched := func(t *testing.T, _ *store.Store) {
		if len(*reached) > 0 {
			t.Errorf("the refused call ran the operation anyway: %v", *reached)
		}
	}

	// Every record-scoped tool needs an entry. A tool the sweep detects but finds
	// no entry for fails below rather than being skipped.
	fixtures := map[string]recordFixture{
		"deploy_project":    {arg: "project_id", inScope: okProject, outOfScope: badProject, verifyUntouched: projectUntouched},
		"down_project":      {arg: "project_id", inScope: okProject, outOfScope: badProject, verifyUntouched: projectUntouched},
		"preview_deploy":    {arg: "project_id", inScope: okProject, outOfScope: badProject, verifyUntouched: projectUntouched},
		"alert_delivery":    {arg: "alert_id", inScope: okAlert, outOfScope: badAlert, verifyUntouched: func(*testing.T, *store.Store) {}},
		"acknowledge_alert": {arg: "id", inScope: okAlert, outOfScope: badAlert, verifyUntouched: alertUntouched},
	}

	mkToken(t, st, uid, "scope-secret", nil, false)
	cs, ctx := connect(t, ts.URL, "scope-secret")
	tools, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	call := func(name, arg string, id int64) (string, bool) {
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: name, Arguments: map[string]any{arg: id},
		})
		text, failed := "", err != nil
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

	var checked, leaky, unregistered []string
	for _, tool := range tools.Tools {
		arg := recordScopedArg(tool.InputSchema)
		if arg == "" {
			continue
		}
		fx, ok := fixtures[tool.Name]
		if !ok {
			unregistered = append(unregistered, tool.Name+" (takes "+arg+", no host_id)")
			continue
		}
		if fx.arg != arg {
			t.Errorf("%s: fixture uses %q but the tool now takes %q", tool.Name, fx.arg, arg)
			continue
		}
		checked = append(checked, tool.Name)

		*reached = (*reached)[:0]
		text, failed := call(tool.Name, arg, fx.outOfScope)
		if !failed {
			leaky = append(leaky, tool.Name+" (succeeded on an out-of-scope host)")
		}
		fx.verifyUntouched(t, st)

		// The mirror: the same tool must still work for an in-scope record, or the
		// gate is simply refusing everything and the sweep proves nothing.
		//
		// Note this is where an OVER-tightened check is caught — one that also
		// demands the "hosts" section would refuse here even though the caller can
		// see the record. Over-tightening is a different bug that looks like
		// caution, not a safe default.
		*reached = (*reached)[:0]
		if _, failed := call(tool.Name, arg, fx.inScope); failed {
			t.Errorf("%s refused a record on an in-scope host", tool.Name)
		}
		_ = text
	}

	if len(checked) == 0 {
		t.Fatal("no advertised tool is record-scoped — the sweep is not exercising anything")
	}
	if len(unregistered) > 0 {
		t.Errorf(`%d tool(s) resolve a host from a record id but have no fixture here.
Add one, so how they are host-scoped is a decision rather than an omission:
  %s`, len(unregistered), strings.Join(unregistered, "\n  "))
	}
	if len(leaky) > 0 {
		t.Errorf(`SECURITY: %d tool(s) did not check their record's host:
  %s`, len(leaky), strings.Join(leaky, "\n  "))
	}
}

// The list must not disclose that an out-of-scope project exists at all. A tool
// that refuses to act on one but happily names it has still leaked the workload
// inventory of a host the caller cannot reach.
func TestListManagedProjectsHidesOutOfScopeHosts(t *testing.T) {
	const inScope, outOfScope = int64(7), int64(8)
	ts, st, uid, _ := newHostScopedServer(t, inScope)
	ctx0 := context.Background()

	for _, name := range []string{"good", "bad"} {
		if _, err := st.CreateHost(ctx0, &store.Host{Name: name, Kind: "tcp", Address: "tcp://127.0.0.1:1"}); err != nil {
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
