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

// TestStackContainersActionRejectsDuplicateIDs proves the MCP-layer guard
// added for Finding 2: a batch naming the same container twice is refused
// with an error naming the duplicate, and — because the rejection happens
// before h.deps.Docker.BulkStackContainerAction is ever called — zero
// containers are acted on. h.deps.Docker is left as its zero value
// (newTestHandler never sets it), so reaching that call here would panic;
// the test passing at all is itself evidence the rejection happened before
// any Docker work was attempted.
func TestStackContainersActionRejectsDuplicateIDs(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	tool := h.stackContainersActionTool("restart")
	in := stackContainersActionInput{Project: "web", ContainerIDs: []string{"c1", "c2", "c1"}}
	_, _, err = tool(ctx, req, in)
	if err == nil {
		t.Fatal("a container_ids batch containing the same id twice should be refused")
	}
	if !strings.Contains(err.Error(), "c1") {
		t.Errorf("the refusal should name the duplicate id: %v", err)
	}
}

// TestStackContainersActionDuplicateRejectionDoesNotChargeExtra proves the
// other half of Finding 2's fix: a rejected duplicate-containing request
// costs no MORE than the 1 unit authorize() already spent — the extra
// per-container charge (Finding 1) must never run for a batch that gets
// refused for duplicates.
func TestStackContainersActionDuplicateRejectionDoesNotChargeExtra(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 5
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	tool := h.stackContainersActionTool("restart")
	// 5 containers, but 3 of them are the same id repeated — if the extra
	// charge ran before the dedup check, this alone would nearly drain a
	// burst of 5. It must instead cost exactly 1 (authorize's own charge).
	in := stackContainersActionInput{Project: "web", ContainerIDs: []string{"c1", "c1", "c1", "c2", "c3"}}
	if _, _, err := tool(ctx, req, in); err == nil {
		t.Fatal("duplicate ids should have been refused")
	}

	// 4 units must still be available: only the 1 unit authorize() spent for
	// this call is gone.
	for i := 0; i < 4; i++ {
		if ok, _ := h.limiter.allow(uid); !ok {
			t.Fatalf("only %d of 4 remaining units were available; the duplicate rejection overspent the limiter", i)
		}
	}
}

// TestStackContainersActionChargesPerAdditionalContainer is the regression
// test for Finding 1: the control rate limiter must charge per CONTAINER
// changed, not per CALL. Exercised directly against chargeAdditionalContainers
// (the function stackContainersActionTool calls after authorize() and the
// dedup check) so the boundary can be proven without a Docker daemon — the
// same "test the pure boundary directly" convention validateStackContainerIDs
// already uses in this file.
//
// The proof: with authorize()'s own 1-unit charge simulated per call, three
// calls of 3 containers each cost 9 units total (3 x [1 authorize + 2 extra])
// and fit inside a burst of 10 with exactly 1 to spare. A fourth such call
// needs 3 more — 1 remains — so it must be refused. If the limiter only
// charged per call (the bug), all 4 calls (12 containers acted on) would have
// succeeded inside the same burst of 10.
func TestStackContainersActionChargesPerAdditionalContainer(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 10
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}

	// spend mirrors what stackContainersActionTool does: authorize()'s own
	// 1-unit charge, then chargeAdditionalContainers for the rest of the
	// batch.
	spend := func(containers int) error {
		if ok, _ := h.limiter.allow(p.user.ID); !ok {
			return errControlRateLimited()
		}
		return h.chargeAdditionalContainers(p, containers)
	}

	for i := 1; i <= 3; i++ {
		if err := spend(3); err != nil {
			t.Fatalf("call %d of 3 (each acting on 3 containers) refused inside a burst of 10: %v", i, err)
		}
	}
	if err := spend(3); err == nil {
		t.Fatal("SECURITY: a 4th 3-container call succeeded inside a burst of 10 " +
			"(12 container changes from 10 units) — the limiter is charging per call, not per container")
	}
}

// TestStackContainersActionSingleContainerCallsCostOne is the contrasting
// case: a batch of exactly 1 container must cost exactly 1 unit (no extra
// charge), so ordinary single-container use is unaffected by this fix.
func TestStackContainersActionSingleContainerCallsCostOne(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 3
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}

	for i := 1; i <= 3; i++ {
		if ok, _ := h.limiter.allow(p.user.ID); !ok {
			t.Fatalf("authorize's own charge %d of 3 refused inside the burst", i)
		}
		if err := h.chargeAdditionalContainers(p, 1); err != nil {
			t.Fatalf("a single-container batch should add no extra charge: %v", err)
		}
	}
	if ok, _ := h.limiter.allow(p.user.ID); ok {
		t.Fatal("a 4th single-container call should have exhausted a burst of 3")
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

// TestChargeAdditionalContainersRefusalChargesNothing proves reserve's
// all-or-nothing charging: DC-SEC-003's second bug was that the old
// one-unit-at-a-time loop left whatever tokens it had already spent in place
// even when the batch as a whole got refused. With 5 tokens left, a batch
// needing 6 must be refused AND leave exactly 5 tokens behind — not fewer.
func TestChargeAdditionalContainersRefusalChargesNothing(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 10
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}

	for i := 0; i < 5; i++ {
		h.limiter.allow(uid)
	} // 10 - 5 = 5 remain

	if err := h.chargeAdditionalContainers(p, 7); err == nil { // needs 6 extra; only 5 remain
		t.Fatal("a batch needing 6 extra tokens with only 5 available should be refused")
	}
	for i := 0; i < 5; i++ {
		if ok, _ := h.limiter.allow(uid); !ok {
			t.Fatalf("only %d of 5 remaining tokens survived a refused batch — the refusal partially charged", i)
		}
	}
	if ok, _ := h.limiter.allow(uid); ok {
		t.Error("a 6th token survived — more than the 5 that should have remained")
	}
}

// TestChargeAdditionalContainersRefusesImpossibleBatchWithDistinctMessage: a
// batch bigger than the burst can never fit even against a fully-fresh
// bucket, so retrying can never help — the message must say so instead of
// the generic "wait and retry" one, which would send a caller into a retry
// loop against a request that can never succeed.
func TestChargeAdditionalContainersRefusesImpossibleBatchWithDistinctMessage(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 30
	u, err := h.deps.Store.UserByID(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}

	err = h.chargeAdditionalContainers(p, 40) // > burst even fully fresh
	if err == nil {
		t.Fatal("a 40-container batch must be refused: no bucket size can ever cover it")
	}
	if strings.Contains(err.Error(), "Wait before retrying") {
		t.Errorf("this refusal is permanent (no bucket size covers it) — must not suggest waiting: %v", err)
	}
}

// TestChargeAdditionalContainersScalesForLargeStacks proves a big batch
// (deliberately > the old per-call flat cost of 1) genuinely costs one unit
// per container, closing the exact "stop a 30+ container stack for one
// token" gap the review flagged for restart_stack/stop_stack.
func TestChargeAdditionalContainersScalesForLargeStacks(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 40
	u, err := h.deps.Store.UserByID(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}

	h.limiter.allow(uid) // authorize()'s own unit for the call itself
	if err := h.chargeAdditionalContainers(p, 31); err != nil {
		t.Fatalf("a 31-container stack should fit in a burst of 40 (1 + 30 extra): %v", err)
	}
	// chargeAdditionalContainers(31) spends 30 (n-1) beyond authorize()'s own
	// 1, so 40 - 1 - 30 = 9 remain.
	for i := 0; i < 9; i++ {
		if ok, _ := h.limiter.allow(uid); !ok {
			t.Fatalf("unit %d of the 9 that should remain is missing", i)
		}
	}
	if ok, _ := h.limiter.allow(uid); ok {
		t.Fatal("the bucket should be fully drained after a 31-container action costing 31 of 40 units")
	}
}

// TestRunStackActionRefusesLargeStackBeforeTouchingDocker proves
// stackActionTool's wiring — not just chargeAdditionalContainers in
// isolation — actually charges per container before reaching Docker.
// h.deps.Docker is nil here (newTestHandler never sets it); StackAction
// would panic if reached, so this passing IS the proof the limiter refused
// first — the same "the test passing at all is the evidence" convention
// TestStackContainersActionRejectsDuplicateIDs already relies on.
func TestRunStackActionRefusesLargeStackBeforeTouchingDocker(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 10 // less than 31
	u, err := h.deps.Store.UserByID(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	p := &principal{user: u}
	ids := make([]string, 31)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%d", i)
	}

	if _, err := h.runStackAction(context.Background(), p, 0, "web", "restart", ids); err == nil {
		t.Fatal("a 31-container stack action should have been refused by the limiter before any Docker call")
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
