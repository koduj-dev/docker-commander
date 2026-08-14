package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNoDestructiveToolsAreAdvertised.
//
// The MCP surface is deliberately read + SAFE control: nothing that destroys
// state. StackAction can also "remove" — force-removing a stack's containers and
// its networks — and adding a tool for it would be one line, which is exactly why
// this test exists. It fails on the ABSENCE of a decision rather than on a known
// bug.
//
// If destructive tools are ever wanted, the agreed route is NOT to add them here.
// It is an explicit opt-in the operator turns on in the UI, off by default, in a
// separate risky toolset, audited, and constrained by both token and role — so
// that "my assistant deleted the stack" can only follow from someone having
// decided it could. Until that exists, this test is the guard.
func TestNoDestructiveToolsAreAdvertised(t *testing.T) {
	ts, st, uid := newDenyAllServer(t)
	mkToken(t, st, uid, "destruct-secret", nil, false)
	cs, ctx := connect(t, ts.URL, "destruct-secret")

	tools, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools advertised — the harness is not exercising anything")
	}

	// Substrings that would indicate an irreversible action. `down_project` is the
	// documented exception: it is `compose down`, which stops and removes
	// containers but keeps named volumes, and it is the counterpart of
	// deploy_project.
	banned := []string{"remove", "delete", "destroy", "prune", "exec", "shell", "kill"}
	for _, tool := range tools.Tools {
		name := strings.ToLower(tool.Name)
		for _, b := range banned {
			if strings.Contains(name, b) {
				t.Errorf("tool %q looks destructive (%q). The MCP surface is read + safe control. "+
					"Destructive tools need an operator-facing opt-in that is OFF by default, a separate risky "+
					"toolset, an audit trail and a docs entry — not a new entry alongside the safe ones.", tool.Name, b)
			}
		}
	}
}

// TestScanImageRejectsArgumentInjection.
//
// The ref is handed to the trivy CLI. A leading '-' would be read as a flag, so a
// ref like "--output=/etc/cron.d/x" turns a scan into a file write. The REST
// handler guards this; the MCP tool reaches the same code and needs the same
// guard, and "it's validated elsewhere" is how that gets missed.
func TestScanImageRejectsArgumentInjection(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	for _, ref := range []string{
		"--output=/tmp/pwned",
		"-o/tmp/pwned",
		"nginx:1.27; rm -rf /",
		"nginx:1.27 --format json",
		"",
		strings.Repeat("a", 600),
	} {
		if _, _, err := h.scanImage(ctx, req, scanImageInput{Ref: ref}); err == nil {
			t.Errorf("SECURITY: scan_image accepted %q; it reaches a CLI argument", ref)
		}
	}
}

// TestScanImageIsAWrite: scanning shells out to trivy and pulls the image if it
// is missing, so it is work performed on the host, not a lookup. A read-only
// token must not be able to trigger it.
func TestScanImageIsAWrite(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}

	ro := reqFor(&principal{user: u, roOnly: true})
	if _, _, err := h.scanImage(ctx, ro, scanImageInput{Ref: "nginx:1.27"}); err == nil {
		t.Error("SECURITY: a read-only token was allowed to start an image scan")
	}

	// The same applies to the stack verbs — they change what is running.
	for _, action := range []string{"start", "stop", "restart"} {
		tool := h.stackActionTool(action)
		if _, _, err := tool(ctx, ro, stackActionInput{Project: "web"}); err == nil {
			t.Errorf("SECURITY: a read-only token was allowed to %s a stack", action)
		}
	}

	// And to the stack-container-subset verbs.
	for _, action := range []string{"restart", "stop"} {
		tool := h.stackContainersActionTool(action)
		in := stackContainersActionInput{Project: "web", ContainerIDs: []string{"abc123"}}
		if _, _, err := tool(ctx, ro, in); err == nil {
			t.Errorf("SECURITY: a read-only token was allowed to %s stack containers", action)
		}
	}
}

// TestStackContainersActionRequiresAProject mirrors
// TestStackActionRequiresAProject: an empty project would otherwise reach
// BulkStackContainerAction, which matches on a label — "act on the stack
// called nothing" is not a request anyone means.
func TestStackContainersActionRequiresAProject(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	tool := h.stackContainersActionTool("restart")
	in := stackContainersActionInput{Project: "  ", ContainerIDs: []string{"abc123"}}
	if _, _, err := tool(ctx, req, in); err == nil {
		t.Error("an empty project name must be refused")
	}
}

// TestStackContainersActionRequiresContainerIDs: an empty container_ids list
// is refused before it reaches BulkStackContainerAction, same input-validation
// convention as the empty-project check above.
func TestStackContainersActionRequiresContainerIDs(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	tool := h.stackContainersActionTool("stop")
	if _, _, err := tool(ctx, req, stackContainersActionInput{Project: "web"}); err == nil {
		t.Error("an empty container_ids must be refused")
	}
}

// TestStackContainersActionEnforcesCap proves the 10-container cap boundary:
// 9 and 10 pass validation, 11 is refused with a message that states the cap
// and points at the whole-stack tool. validateStackContainerIDs is a pure
// function precisely so this boundary can be checked without a handler or a
// Docker daemon behind it.
func TestStackContainersActionEnforcesCap(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("container-%d", i)
		}
		return out
	}

	if err := validateStackContainerIDs(ids(9)); err != nil {
		t.Errorf("9 container_ids should be allowed: %v", err)
	}
	if err := validateStackContainerIDs(ids(maxStackContainerIDs)); err != nil {
		t.Errorf("exactly %d container_ids should be allowed: %v", maxStackContainerIDs, err)
	}

	err := validateStackContainerIDs(ids(maxStackContainerIDs + 1))
	if err == nil {
		t.Fatalf("%d container_ids should be refused (cap is %d)", maxStackContainerIDs+1, maxStackContainerIDs)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(maxStackContainerIDs)) {
		t.Errorf("the cap-exceeded error should state the cap (%d): %v", maxStackContainerIDs, err)
	}
	if !strings.Contains(err.Error(), "restart_stack") || !strings.Contains(err.Error(), "stop_stack") {
		t.Errorf("the cap-exceeded error should point at the whole-stack tools: %v", err)
	}
}

// TestAuditStackContainerResultsAuditsSuccessAndFailure proves the per-container
// audit fan-out: every result, success or failure, gets its own entry under
// the existing mcp.container.<action> code (the same code containerActionTool
// writes — no new audit vocabulary for this tool), matching the granularity
// the REST bulk endpoint already established.
func TestAuditStackContainerResultsAuditsSuccessAndFailure(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}

	results := []docker.BulkActionResult{
		{ID: "container-ok", OK: true},
		{ID: "container-bad", OK: false, Error: "container is paused"},
	}
	if ok := h.auditStackContainerResults(p, "stop", results); ok {
		t.Error("the overall result should be false when any container in the batch failed")
	}

	entries, err := h.deps.Store.RecentAudit(ctx, 500, 0)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	var sawSuccess, sawFailure bool
	for _, e := range entries {
		if e.Action != "mcp.container.stop" {
			continue
		}
		switch e.Target {
		case "container-ok":
			sawSuccess = true
			if strings.Contains(e.Detail, "failed") {
				t.Errorf("container-ok succeeded but its audit entry reads as a failure: %q", e.Detail)
			}
		case "container-bad":
			sawFailure = true
			if !strings.Contains(e.Detail, "failed") {
				t.Errorf("container-bad failed but its audit entry doesn't say so: %q", e.Detail)
			}
		}
	}
	if !sawSuccess {
		t.Error("no audit entry was written for the container that succeeded")
	}
	if !sawFailure {
		t.Error("no audit entry was written for the container that failed")
	}
}

// TestAuditStackContainerResultsAllSucceed: the inverse of the above — when
// every container in the batch succeeds, the overall result is true.
func TestAuditStackContainerResultsAllSucceed(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}

	results := []docker.BulkActionResult{{ID: "a", OK: true}, {ID: "b", OK: true}}
	if ok := h.auditStackContainerResults(p, "restart", results); !ok {
		t.Error("the overall result should be true when every container succeeded")
	}
}

func TestStackActionRequiresAProject(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	// An empty project would otherwise reach StackAction, which matches on a
	// label — "act on the stack called nothing" is not a request anyone means.
	tool := h.stackActionTool("restart")
	if _, _, err := tool(ctx, req, stackActionInput{Project: "  "}); err == nil {
		t.Error("an empty project name must be refused")
	}
}

// TestPreviewDeployIsAReadNotAWrite.
//
// A preview exists to be reached BEFORE the deploy it protects. Gating it as a
// write would mean a read-only token could not look before it leapt — and since
// it cannot leap either, the only effect would be to leave it guessing. It
// resolves the compose file and lists containers; it changes nothing.
func TestPreviewDeployIsAReadNotAWrite(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	h.deps.PreviewProject = func(context.Context, int64) (ProjectPreview, error) {
		called = true
		return ProjectPreview{Valid: true, Project: "web"}, nil
	}

	// A real project row: previewDeploy resolves the project to authorize against
	// the host it targets, so a made-up id is now refused before it gets that far.
	pid, err := h.deps.Store.CreateProject(ctx, &store.Project{
		Name: "web", Slug: "web", ComposeFile: "compose.yml",
	})
	if err != nil {
		t.Fatal(err)
	}

	ro := reqFor(&principal{user: u, roOnly: true})
	if _, out, err := h.previewDeploy(ctx, ro, previewDeployInput{ProjectID: pid}); err != nil {
		t.Fatalf("a read-only token must still be able to preview: %v", err)
	} else if !out.Valid {
		t.Errorf("unexpected result: %+v", out)
	}
	if !called {
		t.Error("the preview closure was never reached")
	}
}

// TestPreviewDeployStillNeedsTheProjectsSection: read-only is not the same as
// unauthenticated, and the section gate still applies.
func TestPreviewDeployStillNeedsTheProjectsSection(t *testing.T) {
	h, uid := newTestHandler(t, denyAllCheckAccess)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	h.deps.PreviewProject = func(context.Context, int64) (ProjectPreview, error) {
		t.Error("SECURITY: the preview ran despite the access gate denying everything")
		return ProjectPreview{}, nil
	}
	if _, _, err := h.previewDeploy(ctx, reqFor(&principal{user: u}), previewDeployInput{ProjectID: 1}); err == nil {
		t.Error("SECURITY: preview_deploy ignored the access gate")
	}
}

func TestPreviewDeployValidatesInput(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	// No closure wired (the host application did not provide one): a clear
	// message beats a nil dereference.
	if _, _, err := h.previewDeploy(ctx, req, previewDeployInput{ProjectID: 1}); err == nil {
		t.Error("an unavailable preview must be reported, not panic")
	}

	h.deps.PreviewProject = func(context.Context, int64) (ProjectPreview, error) {
		t.Error("a bad project id should never reach the closure")
		return ProjectPreview{}, nil
	}
	if _, _, err := h.previewDeploy(ctx, req, previewDeployInput{ProjectID: 0}); err == nil {
		t.Error("project_id is required")
	}
}
