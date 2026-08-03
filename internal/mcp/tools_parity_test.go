package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNoDestructiveToolsAreAdvertised.
//
// The MCP surface is deliberately read + SAFE control: nothing that destroys
// state. StackAction can also "remove" — force-removing a stack's containers and
// its networks — and adding a tool for it would be one line, which is exactly why
// this test exists. It fails on the ABSENCE of a decision rather than on a known
// bug: if a destructive verb ever appears, it should be because someone argued
// for it, not because it was next to the others.
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
				t.Errorf("tool %q looks destructive (%q). The MCP surface is read + safe control; "+
					"if this is intended it needs an explicit decision, an audit trail and a docs entry.", tool.Name, b)
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
