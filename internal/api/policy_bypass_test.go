package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// TestHandleDeployProject_BlockModeCatchesViolationInSelectedProfile is the
// real-CLI proof for the P1 fix: a service gated behind an inactive Compose
// profile is absent from a plain `config --format json`, so evaluating
// policy against that (rather than against the exact profiles the deploy
// selects) would let a blocked violation inside a selected profile through.
// Reproduced here by putting the privileged service behind a "danger"
// profile and selecting it on the deploy request.
func TestHandleDeployProject_BlockModeCatchesViolationInSelectedProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-policy-profile"
	compose := "services:\n" +
		"  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n" +
		"  danger:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    profiles: [\"danger\"]\n    privileged: true\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() { freeDeployStack(slug) })

	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"privileged": "block"}); err != nil {
		t.Fatal(err)
	}

	w := policyDeployRequest(srv, pid, admin, `{"build":false,"profiles":["danger"]}`)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Policy struct {
			Blocked []docker.PolicyViolation `json:"blocked"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("a block-mode violation inside the selected profile must refuse the deploy")
	}
	if len(resp.Policy.Blocked) != 1 || resp.Policy.Blocked[0].Rule != docker.RulePrivileged || resp.Policy.Blocked[0].Service != "danger" {
		t.Errorf("expected exactly one privileged violation on 'danger', got %+v", resp.Policy.Blocked)
	}
	if n := runningServiceCount(t, slug, "danger"); n != 0 {
		t.Errorf("SECURITY: the profile-gated privileged container ran despite a block-mode violation (%d running)", n)
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Errorf("a refused deploy must not bring up any service, including ones outside the violating profile (%d running)", n)
	}
}

// TestMCPDeployProject_BlockModePreventsDeploy is the MCP half of the P1 fix:
// deploy_project used to call ComposeUpFiles directly, bypassing the policy
// gate entirely. It must now refuse exactly like the REST deploy handler.
func TestMCPDeployProject_BlockModePreventsDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-mcp-policy-block"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    privileged: true\n"
	srv, st, pid, _ := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() { freeDeployStack(slug) })

	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"privileged": "block"}); err != nil {
		t.Fatal(err)
	}

	out, err := srv.mcpDeployProject(context.Background(), pid, nil, false)
	if err == nil {
		t.Fatalf("SECURITY: MCP deploy succeeded despite a block-mode privileged violation: %s", out)
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Errorf("SECURITY: the container ran despite a block-mode policy violation via MCP (%d running)", n)
	}
}

// TestMCPDeployProject_WarnModeRequiresConfirmation covers the MCP
// confirmation flow: an un-confirmed warn-mode violation refuses, and
// confirm_policy_warnings=true (surfaced as the confirmPolicyWarnings
// parameter here) lets the deploy proceed.
func TestMCPDeployProject_WarnModeRequiresConfirmation(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-mcp-policy-warn"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n"
	srv, st, pid, _ := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() {
		_, _ = docker.ComposeDown(context.Background(), srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"latest_tag": "warn"}); err != nil {
		t.Fatal(err)
	}

	if _, err := srv.mcpDeployProject(context.Background(), pid, nil, false); err == nil {
		t.Fatal("an un-confirmed warn-mode violation must refuse the MCP deploy")
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Fatalf("the deploy must not have run before confirmation, got %d running", n)
	}

	out, err := srv.mcpDeployProject(context.Background(), pid, nil, true)
	if err != nil {
		t.Fatalf("confirming the warning must let the MCP deploy proceed: %v (%s)", err, out)
	}
	if n := runningServiceCount(t, slug, "web"); n != 1 {
		t.Errorf("expected the deploy to actually run after confirmation, got %d running", n)
	}
}

// TestHandleRestoreRevision_BlockModePreventsDeploy is the revision-restore
// half of the P1 fix: restore used to call ComposeUpFiles directly. A rule
// enabled AFTER a revision was captured must still refuse restoring it.
func TestHandleRestoreRevision_BlockModePreventsDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	// deployTestImage ("alpine:latest") is itself unpinned, so this revision's
	// own compose file trips latest_tag without needing any extra setup.
	const slug = "dctest-restore-policy-block"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() { freeDeployStack(slug) })

	mustDeploy(t, srv, pid, admin, `{"build":false}`)
	if out, err := docker.ComposeDown(context.Background(), srv.projectRoot(pid), slug, nil); err != nil {
		t.Fatalf("tear down before restore: %v (%s)", err, out)
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Fatalf("setup: expected nothing running before the restore attempt, got %d", n)
	}

	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"latest_tag": "block"}); err != nil {
		t.Fatal(err)
	}

	w := restoreRevisionRequest(srv, pid, 1, admin, "admin", "{}")
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Policy struct {
			Blocked []docker.PolicyViolation `json:"blocked"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("a block-mode violation must refuse the restore")
	}
	if len(resp.Policy.Blocked) != 1 || resp.Policy.Blocked[0].Rule != docker.RuleLatestTag {
		t.Errorf("expected exactly one latest_tag violation, got %+v", resp.Policy.Blocked)
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Errorf("SECURITY: the restore deployed despite a block-mode policy violation (%d running)", n)
	}
}
