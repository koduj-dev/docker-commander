package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types/container"

	"github.com/coder/websocket"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// --- validation, no daemon required -----------------------------------------

// TestAPIBulkPullImagesValidation exercises the guards that run before this
// handler ever touches the daemon or upgrades the connection: they must
// respond as a normal HTTP 400, not attempt a WebSocket handshake first.
func TestAPIBulkPullImagesValidation(t *testing.T) {
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
		ids  []string
	}{
		{"missing ids", nil},
		{"empty ids", []string{}},
		{"too many ids", tooMany},
		{"duplicate id", []string{"abc", "abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/api/containers/bulk-pull"
			if tc.ids != nil {
				path += "?ids=" + url.QueryEscape(strings.Join(tc.ids, ","))
			}
			code, resp := a.do("GET", path, nil)
			if code != 400 {
				t.Errorf("%s: got %d, want 400 (%v)", tc.name, code, resp)
			}
		})
	}
}

// --- daemon-backed: fail-closed on an unknown id, and image dedup -----------

// dialBulkPull opens the bulk-pull WebSocket and reads frames until an
// allDone frame (or the connection closes / the deadline hits), returning the
// decoded allDone frame's raw fields.
func dialBulkPull(t *testing.T, a *apiClient, ids []string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(a.url, "http") + "/api/containers/bulk-pull?ids=" + url.QueryEscape(strings.Join(ids, ","))
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: a.c})
	if err != nil {
		t.Fatalf("dial bulk-pull: %v", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var f map[string]any
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if allDone, _ := f["allDone"].(bool); allDone {
			return f
		}
	}
}

// TestAPIBulkPullImagesUnknownContainerRefusesWholeRequest is the fail-closed
// pentest: a batch mixing one real container id with one that doesn't exist
// must refuse the WHOLE request before pulling anything — never pull the
// valid one and skip the invalid one. Proven by asserting a plain HTTP 400
// (the guard runs, and refuses, before the WebSocket upgrade) AND that no
// image.pull audit entry was created — the pull genuinely never ran.
func TestAPIBulkPullImagesUnknownContainerRefusesWholeRequest(t *testing.T) {
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
		Image: "alpine:latest", Name: "dctest_bulkpull_unknown", Cmd: []string{"sleep", "300"}, Start: true,
	})
	if err != nil {
		t.Skipf("cannot create container: %v", err)
	}
	t.Cleanup(func() {
		if cli, err := a.dm.Client(ctx, 0); err == nil {
			_ = cli.ContainerRemove(ctx, id, dockertypes.RemoveOptions{Force: true})
		}
	})

	const bogus = "dctest-bulkpull-does-not-exist"
	code, resp := a.do("GET", "/api/containers/bulk-pull?ids="+url.QueryEscape(id+","+bogus), nil)
	if code != http.StatusBadRequest {
		t.Fatalf("SECURITY: mixed valid/unknown id batch got %d, want 400 (refuse the whole request): %v", code, resp)
	}

	_, entries := a.getJSONArray("/api/audit")
	for _, e := range entries {
		if e["action"] == "image.pull" {
			t.Errorf("SECURITY: an image.pull audit entry exists (%v) even though the request should have been refused before pulling anything", e)
		}
	}
}

// TestAPIBulkPullImagesDedupesSharedImage proves two containers sharing the
// same image are pulled ONCE, not twice: the response names one result for
// the shared ref covering both container ids, and the audit log carries
// exactly one image.pull entry — not one per container.
func TestAPIBulkPullImagesDedupesSharedImage(t *testing.T) {
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
	mk := func(name string) string {
		id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{
			Image: "alpine:latest", Name: name, Cmd: []string{"sleep", "300"}, Start: true,
		})
		if err != nil {
			t.Skipf("cannot create container: %v", err)
		}
		t.Cleanup(func() {
			if cli, err := a.dm.Client(ctx, 0); err == nil {
				_ = cli.ContainerRemove(ctx, id, dockertypes.RemoveOptions{Force: true})
			}
		})
		return id
	}
	id1 := mk("dctest_bulkpull_dedup1")
	id2 := mk("dctest_bulkpull_dedup2")

	final := dialBulkPull(t, a, []string{id1, id2})
	results, ok := final["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %v, want exactly 1 (both containers share one image)", final["results"])
	}
	res, _ := results[0].(map[string]any)
	if res["ref"] != "alpine:latest" {
		t.Errorf("results[0].ref = %v, want %q", res["ref"], "alpine:latest")
	}
	if res["ok"] != true {
		t.Errorf("results[0].ok = %v, want true: %v", res["ok"], res)
	}
	ids, _ := res["containerIds"].([]any)
	got := map[string]bool{}
	for _, v := range ids {
		got[v.(string)] = true
	}
	if !got[id1] || !got[id2] {
		t.Errorf("containerIds = %v, want both %q and %q", ids, id1, id2)
	}

	_, entries := a.getJSONArray("/api/audit")
	var pulls []map[string]any
	for _, e := range entries {
		if e["action"] == "image.pull" {
			pulls = append(pulls, e)
		}
	}
	if len(pulls) != 1 {
		t.Errorf("want exactly 1 image.pull audit entry (deduped), got %d: %v", len(pulls), pulls)
	}
}
