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

func TestLooksLikeBareImageDigest(t *testing.T) {
	const hex64 = "e07d6a1a5a4d8a2e6b3c1f9a0d4e5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a"
	cases := []struct {
		in   string
		want bool
	}{
		{"sha256:" + hex64, true},
		{"nginx:latest", false},
		{"nginx", false},
		// Too short / too long / non-hex must not false-positive.
		{"sha256:abc123", false},
		{"sha256:" + hex64 + "ff", false},
		{"sha256:" + strings.Repeat("g", 64), false},
		// A real repo@digest reference is not a BARE digest.
		{"nginx@sha256:" + hex64, false},
	}
	for _, tc := range cases {
		if got := looksLikeBareImageDigest(tc.in); got != tc.want {
			t.Errorf("looksLikeBareImageDigest(%q) = %v, want %v", tc.in, got, tc.want)
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

// TestAPIBulkPullImagesUntaggedImageGetsAFriendlyError proves a container
// whose image was untagged out from under it (ContainerSummary.Image falls
// back to a bare "sha256:<hex>" digest with no repository name) gets a clear
// per-container failure explaining there's nothing left to pull — not the
// daemon's confusing "repository does not exist" for a bogus ref literally
// named "sha256", which is what normalizeImageRef would otherwise produce by
// misreading the digest's hex half as a tag.
func TestAPIBulkPullImagesUntaggedImageGetsAFriendlyError(t *testing.T) {
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
	const targetImage = "alpine:3.18"
	cli, err := a.dm.Client(ctx, 0)
	if err != nil {
		t.Skipf("cannot get docker client: %v", err)
	}
	if pullErr := a.dm.PullImage(ctx, 0, targetImage, func(docker.PullProgress) {}); pullErr != nil {
		t.Skipf("cannot pull %s: %v", targetImage, pullErr)
	}
	id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{
		Image: targetImage, Name: "dctest_bulkpull_untagged", Cmd: []string{"sleep", "300"},
	})
	if err != nil {
		t.Skipf("cannot create container: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(ctx, id, dockertypes.RemoveOptions{Force: true})
	})
	if _, err := cli.ImageRemove(ctx, targetImage, image.RemoveOptions{Force: true}); err != nil {
		t.Skipf("cannot untag %s: %v", targetImage, err)
	}

	final := dialBulkPull(t, a, []string{id})
	results, ok := final["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %v, want exactly 1", final["results"])
	}
	res, _ := results[0].(map[string]any)
	if res["ok"] == true {
		t.Fatalf("results[0] = %v, want ok:false — the image has no tag left to pull", res)
	}
	errMsg, _ := res["error"].(string)
	if strings.Contains(errMsg, "repository does not exist") || strings.Contains(errMsg, "pull access denied") {
		t.Errorf("error = %q — this is the daemon rejecting a bogus \"sha256\" ref normalizeImageRef built by "+
			"misreading the digest, not the friendly local message", errMsg)
	}
	if !strings.Contains(errMsg, "no tag left to pull") {
		t.Errorf("error = %q, want it to explain the image has no tag left to pull", errMsg)
	}
}

// TestAPIBulkPullImagesStopsOnDisconnect proves a client disconnect (Cancel,
// or the tab closing) actually stops the batch from continuing — not just
// that the client-side socket closes.
//
// Both images are already cached, so ref[0]'s own pull is a fast daemon
// round-trip that may or may not itself get cancelled in time (that race is
// inherent to "how fast can a local TCP close be noticed" and isn't what
// this asserts). What's NOT racy: starting ref[1] requires ref[0] to have
// fully finished first — send its RefDone frame, audit it, append to
// results — which is strictly more elapsed time than the reader goroutine
// needs to have already noticed the close. So ref[1] must never start,
// deterministically, regardless of how the ref[0] race went.
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
	_ = a.dm.PullImage(ctx, 0, "alpine:latest", func(docker.PullProgress) {})
	_ = a.dm.PullImage(ctx, 0, "busybox:latest", func(docker.PullProgress) {})
	mk := func(name, img string) string {
		id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{
			Image: img, Name: name, Cmd: []string{"sleep", "300"}, Start: true,
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
	id0 := mk("dctest_bulkpull_cancel0", "alpine:latest")
	id1 := mk("dctest_bulkpull_cancel1", "busybox:latest")

	wsCtx, wsCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer wsCancel()
	u := "ws" + strings.TrimPrefix(a.url, "http") + "/api/containers/bulk-pull"
	conn, _, err := websocket.Dial(wsCtx, u, &websocket.DialOptions{HTTPClient: a.c})
	if err != nil {
		t.Fatalf("dial bulk-pull: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	body, _ := json.Marshal(map[string]any{"ids": []string{id0, id1}})
	if err := conn.Write(wsCtx, websocket.MessageText, body); err != nil {
		t.Fatalf("send ids: %v", err)
	}
	// Read exactly the first frame (ref[0]'s Started marker), then hang up —
	// the user hitting Cancel right after the pull began.
	if _, _, err := conn.Read(wsCtx); err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	conn.Close(websocket.StatusNormalClosure, "")

	// Give ref[0] time to finish one way or the other (it's a fast, cached
	// pull either way) before asserting ref[1] never got its own entry.
	time.Sleep(2 * time.Second)

	_, entries := a.getJSONArray("/api/audit")
	for _, e := range entries {
		if e["action"] == "image.pull" && e["target"] == "busybox:latest" {
			t.Errorf("SECURITY/correctness: busybox:latest was pulled — the batch must stop at the first "+
				"ref once the client disconnects, not continue to the next one: %v", e)
		}
	}
}
