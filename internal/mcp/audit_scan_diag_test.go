package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// auditEntry returns the newest audit entry with this action, failing the test
// if there is none.
func auditEntry(t *testing.T, st *store.Store, action string) store.AuditEntry {
	t.Helper()
	entries, err := st.RecentAudit(context.Background(), 20, 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action {
			return e
		}
	}
	t.Fatalf("no %s audit entry: %+v", action, entries)
	return store.AuditEntry{}
}

// run_diagnostics is a write (it runs commands on remote hosts over SSH) and the
// REST run is audited; the MCP run must be too, under the host it ran on, and a
// failed run must say it failed.
func TestMCPRunDiagnosticsIsAudited(t *testing.T) {
	st, url, _, _ := mcpFixture(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, &store.User{Username: "diag", PasswordHash: "x", Role: "user", Sections: []string{"diagnostics"}})
	if err != nil {
		t.Fatal(err)
	}
	mkToken(t, st, uid, "diag-token-secret", nil, false)
	cs, cctx := connect(t, url, "diag-token-secret")
	// Nothing listens on port 2, so a run against this host fails.
	unreachable, err := st.CreateHost(ctx, &store.Host{Name: "gone", Kind: "tcp", Address: "tcp://127.0.0.1:2"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cs.CallTool(cctx, &mcpsdk.CallToolParams{Name: "run_diagnostics", Arguments: map[string]any{"host_id": unreachable}}); err != nil {
		t.Fatalf("call: %v", err)
	}
	e := auditEntry(t, st, "mcp.diagnostics.run")
	if e.HostID != unreachable {
		t.Errorf("recorded under host %d, want the host it ran on, %d", e.HostID, unreachable)
	}
	if !strings.Contains(e.Detail, "failed") {
		t.Errorf("a failed run is audited as %q, which doesn't say it failed", e.Detail)
	}
}

// scan_image pulls the image if needed and runs Trivy, and the REST scan is
// audited; the MCP scan must be too, with the outcome. A stand-in `trivy` on
// PATH answers, so this needs neither Trivy nor a daemon. TrivyAvailable caches
// its answer for the process; no other test in this package reaches it.
func TestMCPScanImageIsAudited(t *testing.T) {
	bin := t.TempDir()
	// A clean scan, except for the ref "broken:latest", which fails.
	script := "#!/bin/sh\n[ \"$1\" = version ] && exit 0\n" +
		"for a; do [ \"$a\" = broken:latest ] && { echo 'scan blew up' >&2; exit 1; }; done\n" +
		"echo '{\"Results\":[]}'\n"
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
	local, _ := st.LocalHostID(ctx)

	for _, tc := range []struct {
		ref    string
		failed bool
	}{{"alpine:latest", false}, {"broken:latest", true}} {
		if _, err := cs.CallTool(cctx, &mcpsdk.CallToolParams{Name: "scan_image", Arguments: map[string]any{"ref": tc.ref}}); err != nil {
			t.Fatalf("call %s: %v", tc.ref, err)
		}
		e := auditEntry(t, st, "mcp.image.scan")
		if e.Target != tc.ref || e.HostID != local {
			t.Errorf("entry = target %q host %d, want %s on the local host %d", e.Target, e.HostID, tc.ref, local)
		}
		if got := strings.Contains(e.Detail, "failed"); got != tc.failed {
			t.Errorf("%s: detail %q, want failed=%v", tc.ref, e.Detail, tc.failed)
		}
	}
}
