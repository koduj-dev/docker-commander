package mcp

import (
	"context"
	"os"
	"path/filepath"
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

// scan_image pulls the image if needed and runs Trivy, and the REST scan is
// audited; the MCP scan must be too. A stand-in `trivy` on PATH answers like a
// clean scan, so this needs neither Trivy nor a daemon. TrivyAvailable caches
// its answer for the process; no other test in this package reaches it.
func TestMCPScanImageIsAudited(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = version ] && exit 0\necho '{\"Results\":[]}'\n"
	if err := os.WriteFile(filepath.Join(bin, "trivy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	st, url, _, _ := mcpFixture(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, &store.User{Username: "scanner", PasswordHash: "x", Role: "user", Sections: []string{"images"}})
	if err != nil {
		t.Fatal(err)
	}
	mkToken(t, st, uid, "scan-token-secret", nil, false)
	cs, cctx := connect(t, url, "scan-token-secret")
	if _, err := cs.CallTool(cctx, &mcpsdk.CallToolParams{Name: "scan_image", Arguments: map[string]any{"ref": "alpine:latest"}}); err != nil {
		t.Fatalf("call: %v", err)
	}
	local, _ := st.LocalHostID(ctx)
	entries, err := st.RecentAudit(ctx, 10, 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "mcp.image.scan" {
			if e.Target != "alpine:latest" || e.HostID != local {
				t.Errorf("entry = target %q host %d, want alpine:latest on the local host %d", e.Target, e.HostID, local)
			}
			return
		}
	}
	t.Fatalf("scan_image over MCP left no audit entry: %+v", entries)
}
