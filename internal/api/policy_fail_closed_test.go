package api

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // same CGO-free driver the store package uses

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// P1-3 fix: an indeterminate policy check (store, Compose resolution, or the
// evaluator itself erroring) must never be treated as "no violation" while a
// block-mode rule could have applied. docker.EvaluatePolicy's own error path
// (malformed compose config JSON) is already covered directly by
// TestEvaluatePolicy_InvalidJSONReturnsError in internal/docker/policy_test.go;
// evaluateDeployPolicy handles that error with the exact same anyBlock branch
// as the Compose-resolution error tested below, so it is not re-exercised
// end-to-end here — doing so would need a way to make a real `docker compose
// config` call produce invalid JSON, which nothing in this package can force.

// TestEvaluateDeployPolicy_FailsClosedOnStoreError proves that a
// PolicyRuleModes failure — where the mode of every rule is unknown, so a
// block-mode rule cannot be ruled out — refuses rather than proceeding.
func TestEvaluateDeployPolicy_FailsClosedOnStoreError(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st}
	st.Close() // sabotage: every subsequent store call now errors

	blocked, warned, err := srv.evaluateDeployPolicy(context.Background(), "does-not-exist", "/nonexistent/path/for/sure", nil, nil, nil)
	if err == nil {
		t.Fatal("SECURITY: evaluateDeployPolicy proceeded despite being unable to load policy rule modes")
	}
	if len(blocked) != 0 || len(warned) != 0 {
		t.Errorf("expected no violations reported alongside an error, got blocked=%v warned=%v", blocked, warned)
	}
}

// TestEvaluateDeployPolicy_FailsClosedOnComposeResolutionErrorWhenBlockActive
// proves that when a block-mode rule is configured, a Compose-resolution
// failure refuses rather than silently letting the deploy through.
func TestEvaluateDeployPolicy_FailsClosedOnComposeResolutionErrorWhenBlockActive(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"privileged": "block"}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st}

	blocked, warned, err := srv.evaluateDeployPolicy(context.Background(), "does-not-exist", "/nonexistent/path/for/sure", nil, nil, nil)
	if err == nil {
		t.Fatal("SECURITY: evaluateDeployPolicy proceeded despite an unresolved Compose config while a block-mode rule was active")
	}
	if len(blocked) != 0 || len(warned) != 0 {
		t.Errorf("expected no violations reported alongside an error, got blocked=%v warned=%v", blocked, warned)
	}
}

// TestEvaluateDeployPolicy_ProceedsOnComposeResolutionErrorWhenOnlyWarnActive
// is the deliberate other half: with no block-mode rule active, a broken
// policy check must not itself become the reason a deploy is refused.
func TestEvaluateDeployPolicy_ProceedsOnComposeResolutionErrorWhenOnlyWarnActive(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"privileged": "warn"}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st}

	blocked, warned, err := srv.evaluateDeployPolicy(context.Background(), "does-not-exist", "/nonexistent/path/for/sure", nil, nil, nil)
	if err != nil {
		t.Fatalf("a Compose-resolution error with only warn-mode rules active must not refuse: %v", err)
	}
	if len(blocked) != 0 || len(warned) != 0 {
		t.Errorf("expected no violations when the check itself failed, got blocked=%v warned=%v", blocked, warned)
	}
}

// TestPolicyCheckOrRefuse_RefusesAndAuditsOnCheckFailure covers both HTTP
// callers (deploy, restore): each must refuse and audit under its own
// "…policy_check_failed" action rather than the generic policy_block one,
// so an operator can tell "a rule fired" apart from "the check itself broke".
func TestPolicyCheckOrRefuse_RefusesAndAuditsOnCheckFailure(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		action string
	}{
		{policyKindDeploy, "project.deploy.policy_check_failed"},
		{policyKindRestore, "project.revision.restore.policy_check_failed"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			st, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"privileged": "block"}); err != nil {
				t.Fatal(err)
			}
			srv := &Server{store: st}
			p := &store.Project{Slug: "does-not-exist"}

			resp, refused := srv.policyCheckOrRefuse(httptest.NewRequest("POST", "/x", nil), p, "/nonexistent/path/for/sure", nil, nil, nil, false, tc.kind)
			if !refused {
				t.Fatalf("SECURITY: expected a refusal when the policy check itself fails, got resp=%+v", resp)
			}
			if resp == nil || resp["ok"] != false {
				t.Errorf("expected {ok:false, ...}, got %+v", resp)
			}

			entries, err := st.RecentAudit(context.Background(), 50, 0)
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, e := range entries {
				if e.Action == tc.action && e.Target == p.Slug {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a %q audit entry for %q", tc.action, p.Slug)
			}
		})
	}
}

// TestMCPDeployProject_PolicyCheckFailureRefusesDeploy is the MCP half: a
// broken policy check must refuse a deploy attempted through the MCP tool
// path exactly like the REST handlers, not just log and proceed.
//
// Sabotage: a second raw connection to the SAME sqlite file drops the
// `settings` table PolicyRuleModes reads from, the way
// TestHandleDeployProject_PersistFailureIsLoggedNotSwallowed sabotages one
// column with a trigger — but here the whole table, since there is no row to
// scope a trigger to. A local-host project's projectDeployEnv (called by
// mcpDeployProject before the policy check) never touches `settings`, so
// this fails ONLY the policy check itself, not the deploy's own project/host
// lookups — closing the whole store, or corrupting the compose file, would
// also break docker.ComposeUpFiles right after and pass even without the fix.
func TestMCPDeployProject_PolicyCheckFailureRefusesDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the docker compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-mcp-policy-check-failed"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    privileged: true\n"
	dbPath := filepath.Join(t.TempDir(), "test.db")
	srv, st, pid, _ := deployTestServerAt(t, dbPath, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() { freeDeployStack(slug) })

	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"privileged": "block"}); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite conn: %v", err)
	}
	if _, err := raw.Exec("DROP TABLE settings"); err != nil {
		t.Fatalf("drop settings table: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite conn: %v", err)
	}

	out, err := srv.mcpDeployProject(context.Background(), pid, nil, false)
	if err == nil {
		t.Fatalf("SECURITY: MCP deploy succeeded despite the policy check itself failing (block-mode rule was configured): %s", out)
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Errorf("SECURITY: the container ran despite a failed policy check via MCP (%d running)", n)
	}
}
