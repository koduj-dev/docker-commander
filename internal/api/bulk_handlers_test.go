package api

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// --- validation, no daemon required -----------------------------------------

// TestAPIBulkContainerActionValidation exercises the guard that keeps this
// endpoint narrow: only "restart" and "stop" are accepted, not the full
// ContainerAction verb set (start/pause/unpause/kill), and malformed batches
// are rejected before anything reaches the daemon.
func TestAPIBulkContainerActionValidation(t *testing.T) {
	a := newAPI(t)
	if code, _ := a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"}); code != 200 {
		t.Fatal("setup failed")
	}

	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = "x"
	}

	cases := []struct {
		name string
		body map[string]any
	}{
		// "kill" and "pause" are real ContainerAction verbs, but not ones this
		// endpoint exposes — this is the "don't build a generic
		// action-agnostic bulk endpoint" guard, not a generic bad-input check.
		{"disallowed action (kill)", map[string]any{"ids": []string{"x"}, "action": "kill"}},
		{"disallowed action (pause)", map[string]any{"ids": []string{"x"}, "action": "pause"}},
		{"unknown action", map[string]any{"ids": []string{"x"}, "action": "nope"}},
		{"missing action", map[string]any{"ids": []string{"x"}}},
		{"empty ids", map[string]any{"ids": []string{}, "action": "stop"}},
		{"missing ids", map[string]any{"action": "stop"}},
		{"too many ids", map[string]any{"ids": tooMany, "action": "stop"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := a.do("POST", "/api/containers/bulk-action", tc.body)
			if code != 400 {
				t.Errorf("%s: got %d, want 400 (%v)", tc.name, code, resp)
			}
		})
	}
}

// TestAPIBulkContainerActionRestartAndStopAreAllowed proves the inverse of the
// validation test: "restart", "stop", and "start" are not rejected by the
// action guard. Without a daemon the call still fails downstream (no such
// container), but that must be a 200 with a per-item error, never the 400 the
// disallowed actions get — that distinction is exactly what the guard is for.
func TestAPIBulkContainerActionRestartAndStopAreAllowed(t *testing.T) {
	a := newAPI(t)
	if code, _ := a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"}); code != 200 {
		t.Fatal("setup failed")
	}
	for _, action := range []string{"restart", "stop", "start"} {
		code, resp := a.do("POST", "/api/containers/bulk-action", map[string]any{"ids": []string{"nope"}, "action": action})
		if code != 200 {
			t.Errorf("action %q: got %d, want 200 (%v)", action, code, resp)
		}
	}
}

// --- daemon-backed: summary shape + per-container audit ----------------------

// TestAPIBulkContainerActionSummaryAndAudit drives a real batch containing one
// container that exists and one that does not, over HTTP, and checks:
//   - the response reports one result per id, in order, with the right
//     succeeded/failed counts (a structured summary, not a single ok/fail)
//   - both the container that actually restarted AND the one that failed get
//     their own container.restart audit entry — one entry per container acted
//     on (not a single aggregate "bulk" entry that would hide which container
//     was actually touched), and a failed attempt must leave a trace too: a
//     bulk write against ids the caller doesn't actually control should not be
//     invisible in the audit log just because the daemon rejected it.
func TestAPIBulkContainerActionSummaryAndAudit(t *testing.T) {
	a := newAPI(t)
	if code, _ := a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"}); code != 200 {
		t.Fatal("setup failed")
	}
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	if code, _ := a.do("GET", "/api/system", nil); code != 200 {
		t.Skipf("docker daemon not available (%d)", code)
	}
	ctx := context.Background()
	_ = a.dm.PullImage(ctx, 0, "alpine:latest", func(docker.PullProgress) {})
	id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{
		Image: "alpine:latest", Name: "dctest_apibulk", Cmd: []string{"sleep", "300"}, Start: true,
	})
	if err != nil {
		t.Skipf("cannot create container: %v", err)
	}
	t.Cleanup(func() {
		if cli, err := a.dm.Client(ctx, 0); err == nil {
			_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}
	})

	const bogus = "dctest-apibulk-does-not-exist"
	code, resp := a.do("POST", "/api/containers/bulk-action", map[string]any{
		"ids": []string{id, bogus}, "action": "restart",
	})
	if code != 200 {
		t.Fatalf("bulk-action: %d %v", code, resp)
	}
	if resp["succeeded"] != float64(1) {
		t.Errorf("succeeded = %v, want 1", resp["succeeded"])
	}
	if resp["failed"] != float64(1) {
		t.Errorf("failed = %v, want 1", resp["failed"])
	}
	results, ok := resp["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %v, want a 2-element array", resp["results"])
	}
	r0, _ := results[0].(map[string]any)
	r1, _ := results[1].(map[string]any)
	if r0["id"] != id || r0["ok"] != true {
		t.Errorf("results[0] = %v, want {id:%q ok:true}", r0, id)
	}
	if r1["id"] != bogus || r1["ok"] != false || r1["error"] == nil || r1["error"] == "" {
		t.Errorf("results[1] = %v, want {id:%q ok:false error:<non-empty>}", r1, bogus)
	}

	// Audit: exactly two container.restart entries, one per container acted
	// on — the one that actually restarted AND the one that failed. A bulk
	// write must not drop failed attempts from the audit log.
	_, entries := a.getJSONArray("/api/audit")
	var restarts []map[string]any
	for _, e := range entries {
		if e["action"] == "container.restart" {
			restarts = append(restarts, e)
		}
	}
	if len(restarts) != 2 {
		t.Fatalf("want exactly 2 container.restart audit entries, got %d: %v", len(restarts), restarts)
	}
	var okEntry, failEntry map[string]any
	for _, e := range restarts {
		switch e["target"] {
		case id:
			okEntry = e
		case bogus:
			failEntry = e
		}
	}
	if okEntry == nil {
		t.Errorf("no audit entry for the succeeded container %q: %v", id, restarts)
	} else if d, _ := okEntry["detail"].(string); d != "" {
		t.Errorf("succeeded entry detail = %q, want empty", d)
	}
	if failEntry == nil {
		t.Fatalf("no audit entry for the failed container %q — failed bulk attempts must not be silently dropped from the audit log: %v", bogus, restarts)
	}
	detail, _ := failEntry["detail"].(string)
	if detail == "" {
		t.Errorf("failed entry detail is empty, want the daemon's error text identifying why %q failed", bogus)
	}
}

// TestAPIBulkContainerActionStartWorksEndToEnd proves "start" is not merely
// accepted by the validation guard but actually reaches the daemon and starts
// a stopped container, auditing container.start — the same end-to-end
// standard TestAPIBulkContainerActionSummaryAndAudit holds restart to.
func TestAPIBulkContainerActionStartWorksEndToEnd(t *testing.T) {
	a := newAPI(t)
	if code, _ := a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"}); code != 200 {
		t.Fatal("setup failed")
	}
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	if code, _ := a.do("GET", "/api/system", nil); code != 200 {
		t.Skipf("docker daemon not available (%d)", code)
	}
	ctx := context.Background()
	_ = a.dm.PullImage(ctx, 0, "alpine:latest", func(docker.PullProgress) {})
	// Created but not started: bulk "start" is what's under test here.
	id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{
		Image: "alpine:latest", Name: "dctest_apibulkstart", Cmd: []string{"sleep", "300"},
	})
	if err != nil {
		t.Skipf("cannot create container: %v", err)
	}
	t.Cleanup(func() {
		if cli, err := a.dm.Client(ctx, 0); err == nil {
			_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}
	})

	code, resp := a.do("POST", "/api/containers/bulk-action", map[string]any{"ids": []string{id}, "action": "start"})
	if code != 200 {
		t.Fatalf("bulk-action start: %d %v", code, resp)
	}
	if resp["succeeded"] != float64(1) {
		t.Fatalf("succeeded = %v, want 1: %v", resp["succeeded"], resp)
	}

	_, entries := a.getJSONArray("/api/audit")
	found := false
	for _, e := range entries {
		if e["action"] == "container.start" && e["target"] == id {
			found = true
		}
	}
	if !found {
		t.Errorf("no container.start audit entry for %q: %v", id, entries)
	}
}
