package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func getPolicyRulesRequest(srv *Server, uid int64, role string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/policy-rules", nil).WithContext(ctxAs(uid, role))
	w := httptest.NewRecorder()
	srv.handleGetPolicyRules(w, r)
	return w
}

func setPolicyRulesRequest(srv *Server, uid int64, role, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PUT", "/api/policy-rules", strings.NewReader(body)).WithContext(ctxAs(uid, role))
	w := httptest.NewRecorder()
	srv.handleSetPolicyRules(w, r)
	return w
}

func TestHandleGetPolicyRules_DefaultsToOff(t *testing.T) {
	srv, st, _, admin := deployTestServer(t, "dctest-policy-get", "services:\n  web:\n    image: alpine:latest\n")
	_ = st
	w := getPolicyRulesRequest(srv, admin, "admin")
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Rules []string          `json:"rules"`
		Modes map[string]string `json:"modes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Rules) != 7 {
		t.Errorf("expected 7 rules, got %d: %v", len(resp.Rules), resp.Rules)
	}
	for _, rule := range resp.Rules {
		if resp.Modes[rule] != "off" {
			t.Errorf("rule %q default mode = %q, want off", rule, resp.Modes[rule])
		}
	}
}

func TestHandleSetPolicyRules_PersistsAndAudits(t *testing.T) {
	srv, st, _, admin := deployTestServer(t, "dctest-policy-set", "services:\n  web:\n    image: alpine:latest\n")
	w := setPolicyRulesRequest(srv, admin, "admin", `{"privileged":"block","latest_tag":"warn"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	modes, err := st.PolicyRuleModes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if modes["privileged"] != "block" || modes["latest_tag"] != "warn" {
		t.Errorf("modes not persisted correctly: %v", modes)
	}
	entries, err := st.RecentAudit(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "policy.rules.update" {
			found = true
		}
	}
	if !found {
		t.Error("expected a policy.rules.update audit entry")
	}
}

// A non-admin (even one holding every ordinary section) must be refused by
// checkAccess for the policy-rules section — configuring what a deploy is
// allowed to do is an instance-wide, admin-only decision, the same bucket as
// LDAP/SMTP/users.
func TestPentestPolicyRulesSectionForPath_IsAdminOnly(t *testing.T) {
	for _, path := range []string{"/api/policy-rules"} {
		if got := sectionForPath(path); got != "__admin" {
			t.Errorf("sectionForPath(%q) = %q, want \"__admin\"", path, got)
		}
	}
}

func TestPentestPolicyRulesCheckAccess_NonAdminRefused(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, &store.User{Username: "poweruser", Role: "user", Sections: store.Sections})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st}
	if err := srv.checkAccess(ctx, u, "__admin", true, 0); err == nil {
		t.Error("SECURITY: a non-admin holding every ordinary section still reached the __admin-gated policy-rules section")
	}
}

// policyDeployRequest is deployRequest (project_deploy_test.go) with an
// explicit route context of its own so these tests don't depend on that
// file's helper staying compatible.
func policyDeployRequest(srv *Server, pid, uid int64, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/deploy", strings.NewReader(body)).
		WithContext(ctxAs(uid, "admin"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(pid, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	srv.handleDeployProject(w, r)
	return w
}

// TestHandleDeployProject_BlockModePreventsDeploy is the real proof that a
// block-mode violation stops the deploy: ComposeUpFiles is never invoked
// (asserted the same way the rest of this test file proves any deploy
// behaviour — by checking what's actually running on the real daemon), and
// the response reports the block instead of a success/failure from `up`.
func TestHandleDeployProject_BlockModePreventsDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-policy-block"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n    privileged: true\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() { freeDeployStack(slug) })

	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"privileged": "block"}); err != nil {
		t.Fatal(err)
	}

	w := policyDeployRequest(srv, pid, admin, `{"build":false}`)
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
		t.Fatal("a block-mode violation must refuse the deploy")
	}
	if len(resp.Policy.Blocked) != 1 || resp.Policy.Blocked[0].Rule != docker.RulePrivileged {
		t.Errorf("expected exactly one privileged violation reported, got %+v", resp.Policy.Blocked)
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Errorf("SECURITY: the container ran despite a block-mode policy violation (%d running)", n)
	}

	entries, err := st.RecentAudit(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "project.deploy.policy_block" && e.Target == slug {
			found = true
		}
	}
	if !found {
		t.Error("expected a project.deploy.policy_block audit entry")
	}
}

// TestHandleDeployProject_WarnModeRequiresConfirmationThenProceeds covers both
// halves of the warn-mode contract: an un-confirmed request is refused with
// needsConfirmation, and confirming it lets the deploy through and audits the
// acknowledgement.
func TestHandleDeployProject_WarnModeRequiresConfirmationThenProceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	const slug = "dctest-policy-warn"
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    command: [\"sleep\", \"300\"]\n"
	srv, st, pid, admin := deployTestServer(t, slug, compose)
	freeDeployStack(slug)
	t.Cleanup(func() {
		_, _ = docker.ComposeDown(context.Background(), srv.projectRoot(pid), slug, nil)
		freeDeployStack(slug)
	})

	if err := st.SetPolicyRuleModes(context.Background(), map[string]string{"latest_tag": "warn"}); err != nil {
		t.Fatal(err)
	}

	w := policyDeployRequest(srv, pid, admin, `{"build":false}`)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK                bool `json:"ok"`
		NeedsConfirmation bool `json:"needsConfirmation"`
		Policy            struct {
			Warnings []docker.PolicyViolation `json:"warnings"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || !resp.NeedsConfirmation {
		t.Fatalf("an un-confirmed warn-mode violation must refuse with needsConfirmation, got %s", w.Body.String())
	}
	if len(resp.Policy.Warnings) != 1 || resp.Policy.Warnings[0].Rule != docker.RuleLatestTag {
		t.Errorf("expected exactly one latest_tag warning, got %+v", resp.Policy.Warnings)
	}
	if n := runningServiceCount(t, slug, "web"); n != 0 {
		t.Fatalf("the deploy must not have run before confirmation, got %d running", n)
	}

	w2 := policyDeployRequest(srv, pid, admin, `{"build":false,"confirmPolicyWarnings":true}`)
	if w2.Code != 200 {
		t.Fatalf("status = %d: %s", w2.Code, w2.Body.String())
	}
	var resp2 struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if !resp2.OK {
		t.Fatalf("confirming the warning must let the deploy proceed: %s", w2.Body.String())
	}
	if n := runningServiceCount(t, slug, "web"); n != 1 {
		t.Errorf("expected the deploy to actually run after confirmation, got %d running", n)
	}

	entries, err := st.RecentAudit(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "project.deploy.policy_warn_ack" && e.Target == slug {
			found = true
		}
	}
	if !found {
		t.Error("expected a project.deploy.policy_warn_ack audit entry")
	}
}

// TestPolicyCheckOrRefuse_AllOffSkipsEvaluationEntirely is the latency
// guarantee at the unit level: with every rule off, policyCheckOrRefuse must
// return before ever touching Docker/compose at all — proven by pointing it
// at a project directory that doesn't exist, and slug that isn't a real
// compose project. If it attempted ComposeConfigJSON, that call would need
// the compose CLI and a real directory; with every rule off it must never
// get there, so this passes even without a docker daemon.
func TestPolicyCheckOrRefuse_AllOffSkipsEvaluationEntirely(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{store: st}
	p := &store.Project{Slug: "does-not-exist"}

	start := time.Now()
	resp, refused := srv.policyCheckOrRefuse(httptest.NewRequest("POST", "/x", nil), p, "/nonexistent/path/for/sure", nil, nil, nil, false, policyKindDeploy)
	elapsed := time.Since(start)

	if refused {
		t.Fatalf("expected no refusal with every rule off, got %+v", resp)
	}
	if resp != nil {
		t.Errorf("expected a nil response, got %+v", resp)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("policyCheckOrRefuse took %s with every rule off — it should return immediately without shelling out", elapsed)
	}
}
