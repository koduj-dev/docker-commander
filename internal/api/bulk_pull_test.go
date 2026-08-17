package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"

	"github.com/coder/websocket"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// --- validation, no daemon required -----------------------------------------

func TestNormalizeImageRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx", "nginx:latest"},
		{"nginx:latest", "nginx:latest"},
		{"nginx:1.27", "nginx:1.27"},
		{"library/nginx", "library/nginx:latest"},
		{"ghcr.io/koduj-dev/docker-commander", "ghcr.io/koduj-dev/docker-commander:latest"},
		{"ghcr.io/koduj-dev/docker-commander:v1.6.0", "ghcr.io/koduj-dev/docker-commander:v1.6.0"},
		// A registry host:port must never be mistaken for a tag.
		{"localhost:5000/nginx", "localhost:5000/nginx:latest"},
		{"localhost:5000/nginx:1.27", "localhost:5000/nginx:1.27"},
		// Digest references are left untouched (no tag applies).
		{"nginx@sha256:abc123", "nginx@sha256:abc123"},
	}
	for _, tc := range cases {
		if got := normalizeImageRef(tc.in); got != tc.want {
			t.Errorf("normalizeImageRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// dialBulkPull opens the bulk-pull WebSocket, sends {ids} as the first
// message (the real wire protocol — see bulkPullRequest's doc comment for why
// ids travel in a message rather than the connection URL), then reads frames
// until an allDone frame (or the deadline hits), returning its raw fields.
// Works for both the success and validation-failure paths: a validation
// failure is itself sent as an allDone frame carrying only "error".
func dialBulkPull(t *testing.T, a *apiClient, ids []string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(a.url, "http") + "/api/containers/bulk-pull"
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: a.c})
	if err != nil {
		t.Fatalf("dial bulk-pull: %v", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	body, err := json.Marshal(map[string]any{"ids": ids})
	if err != nil {
		t.Fatalf("marshal ids: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		t.Fatalf("send ids: %v", err)
	}

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

// TestAPIBulkPullImagesValidation exercises the guards that run before this
// handler touches the daemon: they must come back as an allDone frame
// carrying "error", never a partial pull.
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
			f := dialBulkPull(t, a, tc.ids)
			errMsg, _ := f["error"].(string)
			if errMsg == "" {
				t.Errorf("%s: got no error, want the request refused: %v", tc.name, f)
			}
			if _, hasResults := f["results"]; hasResults {
				t.Errorf("%s: got results %v on a request that should have been refused", tc.name, f["results"])
			}
		})
	}
}

// --- daemon-backed: fail-closed on an unknown id, and image dedup -----------

// TestAPIBulkPullImagesUnknownContainerRefusesWholeRequest is the fail-closed
// pentest: a batch mixing one real container id with one that doesn't exist
// must refuse the WHOLE request before pulling anything — never pull the
// valid one and skip the invalid one. Proven by asserting the connection ends
// in an error (never a partial "results") AND that no image.pull audit entry
// was created — the pull genuinely never ran.
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
	f := dialBulkPull(t, a, []string{id, bogus})
	errMsg, _ := f["error"].(string)
	if errMsg == "" {
		t.Fatalf("SECURITY: mixed valid/unknown id batch was not refused: %v", f)
	}
	if _, hasResults := f["results"]; hasResults {
		t.Errorf("SECURITY: got partial results %v — the whole request must be refused, not just the bad id skipped", f["results"])
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

// TestAPIBulkPullImagesDedupesUntaggedSpelling proves dedup survives the same
// image being spelled two ways: one container created from a bare "alpine"
// (no explicit tag) and one from "alpine:latest" run the identical image, and
// without normalizeImageRef they'd read as two distinct ContainerSummary.Image
// strings and pull twice.
func TestAPIBulkPullImagesDedupesUntaggedSpelling(t *testing.T) {
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
	mk := func(name, image string) string {
		id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{
			Image: image, Name: name, Cmd: []string{"sleep", "300"}, Start: true,
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
	idBare := mk("dctest_bulkpull_bare", "alpine")
	idTagged := mk("dctest_bulkpull_tagged", "alpine:latest")

	final := dialBulkPull(t, a, []string{idBare, idTagged})
	results, ok := final["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %v, want exactly 1 (\"alpine\" and \"alpine:latest\" are the same image)", final["results"])
	}
}

// TestAPIBulkPullImagesStopsOnDisconnect proves a client disconnect (Cancel,
// or the tab closing) actually aborts the in-flight pull and stops the batch
// from continuing — not just that the client-side socket closes.
//
// This needs a real timing window to be meaningful: if the target image were
// already cached locally, PullImage would return near-instantly regardless
// of whether the disconnect was noticed, and the test would prove nothing.
// So the target image is explicitly evicted from the local daemon first,
// forcing an actual registry round-trip, which comfortably outlasts how long
// a local TCP close takes to be noticed.
func TestAPIBulkPullImagesStopsOnDisconnect(t *testing.T) {
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

	// A distinct, otherwise-unused tag so evicting it can't affect any other
	// test's fixtures. Pulled once so a container can reference it, then
	// evicted again right before the WS dial — ContainerCreate requires the
	// image to exist locally, but PullImage doesn't, so this ordering is the
	// only way to get "container exists, image locally absent."
	const targetImage = "alpine:3.19"
	cli, err := a.dm.Client(ctx, 0)
	if err != nil {
		t.Skipf("cannot get docker client: %v", err)
	}
	if pullErr := a.dm.PullImage(ctx, 0, targetImage, func(docker.PullProgress) {}); pullErr != nil {
		t.Skipf("cannot pull %s: %v", targetImage, pullErr)
	}
	id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{
		Image: targetImage, Name: "dctest_bulkpull_cancel", Cmd: []string{"sleep", "300"},
	})
	if err != nil {
		t.Skipf("cannot create container: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(ctx, id, dockertypes.RemoveOptions{Force: true})
	})
	if _, err := cli.ImageRemove(ctx, targetImage, image.RemoveOptions{Force: true}); err != nil {
		t.Skipf("cannot evict %s to force a fresh pull: %v", targetImage, err)
	}

	wsCtx, wsCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer wsCancel()
	u := "ws" + strings.TrimPrefix(a.url, "http") + "/api/containers/bulk-pull"
	conn, _, err := websocket.Dial(wsCtx, u, &websocket.DialOptions{HTTPClient: a.c})
	if err != nil {
		t.Fatalf("dial bulk-pull: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	body, _ := json.Marshal(map[string]any{"ids": []string{id}})
	if err := conn.Write(wsCtx, websocket.MessageText, body); err != nil {
		t.Fatalf("send ids: %v", err)
	}
	// Read exactly the Started marker for the (freshly-evicted, still
	// downloading) image, then hang up immediately — the user hitting
	// Cancel right after the pull began.
	if _, _, err := conn.Read(wsCtx); err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	conn.Close(websocket.StatusNormalClosure, "")

	// Poll rather than a single fixed sleep: the audit write happens on the
	// server shortly after the disconnect is noticed, not synchronously with
	// the client's close() call.
	var pulls []map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, entries := a.getJSONArray("/api/audit")
		pulls = nil
		for _, e := range entries {
			if e["action"] == "image.pull" {
				pulls = append(pulls, e)
			}
		}
		if len(pulls) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(pulls) != 1 {
		t.Fatalf("want exactly 1 image.pull audit entry, got %d: %v", len(pulls), pulls)
	}
	detail, _ := pulls[0]["detail"].(string)
	if !strings.Contains(detail, "cancelled") {
		t.Errorf("the entry's detail = %q, want it to say the pull was cancelled by a client disconnect — "+
			"if it succeeded instead, the disconnect was not actually noticed until after the pull completed", detail)
	}
}
