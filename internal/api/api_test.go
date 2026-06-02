package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/pquerna/otp/totp"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/ws"
)

// --- unit tests for the pure helpers ----------------------------------------

func TestSectionForPath(t *testing.T) {
	cases := map[string]string{
		"/api/containers":          "containers",
		"/api/containers/abc/exec": "containers",
		"/api/images/pull":         "images",
		"/api/volumes":             "volumes",
		"/api/networks/x":          "networks",
		"/api/topology":            "topology",
		"/api/parse-rules":         "logs",
		"/api/alert-rules/1":       "alerts",
		"/api/smtp/test":           "alerts",
		"/api/hosts/2":             "hosts",
		"/api/registries":          "registries",
		"/api/audit":               "audit",
		"/api/users":               "__admin",
		"/api/settings":            "__admin",
		"/api/ldap":                "__admin",
		"/api/system":              "", // ungated
		"/api/ws":                  "",
		"/api/auth/me":             "",
	}
	for path, want := range cases {
		if got := sectionForPath(path); got != want {
			t.Errorf("sectionForPath(%q) = %q want %q", path, got, want)
		}
	}
}

func TestIsWriteRequest(t *testing.T) {
	w := func(method, path string) bool {
		return isWriteRequest(httptest.NewRequest(method, path, nil))
	}
	if w(http.MethodGet, "/api/containers") {
		t.Error("GET list is not a write")
	}
	if !w(http.MethodPost, "/api/containers") || !w(http.MethodDelete, "/api/images") {
		t.Error("POST/DELETE are writes")
	}
	if !w(http.MethodGet, "/api/containers/x/exec") || !w(http.MethodGet, "/api/images/pull") {
		t.Error("exec/pull GETs are writes")
	}
}

func TestIsLoopback(t *testing.T) {
	lb := httptest.NewRequest("GET", "/", nil)
	lb.RemoteAddr = "127.0.0.1:5555"
	if !isLoopback(lb) {
		t.Error("127.0.0.1 is loopback")
	}
	rem := httptest.NewRequest("GET", "/", nil)
	rem.RemoteAddr = "10.0.0.4:5555"
	if isLoopback(rem) {
		t.Error("10.0.0.4 is not loopback")
	}
}

func TestRespondHelpers(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]int{"n": 1})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("writeJSON headers/code wrong: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	writeErr(rec, http.StatusBadRequest, "nope")
	if rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte("nope")) {
		t.Errorf("writeErr wrong: %d %s", rec.Code, rec.Body)
	}
	var dst struct {
		A int `json:"a"`
	}
	r := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"a":5}`))
	if err := decodeJSON(r, &dst); err != nil || dst.A != 5 {
		t.Errorf("decodeJSON: %v %+v", err, dst)
	}
	r = httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"unknown":1}`))
	if err := decodeJSON(r, &dst); err == nil {
		t.Error("decodeJSON should reject unknown fields")
	}
}

// --- integration test over the real HTTP handler ----------------------------

type apiClient struct {
	t   *testing.T
	c   *http.Client
	url string
	dm  *docker.Manager
	st  *store.Store
}

func newAPI(t *testing.T) *apiClient {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cph, _ := crypto.New(key)
	st.SetCipher(cph)
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	tm := auth.NewTokenManager(secret, time.Hour)
	dm := docker.NewManager(st)
	srv := NewServer(config.Config{}, st, auth.NewService(st, tm), auth.NewMiddleware(tm),
		dm, ws.NewHub(dm), monitor.New(st, dm, nil), history.Open(context.Background(), history.Config{}), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &apiClient{t: t, c: &http.Client{Jar: jar}, url: ts.URL, dm: dm, st: st}
}

func (a *apiClient) do(method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.url+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestAPIAuthAndAdminEndpoints(t *testing.T) {
	a := newAPI(t)

	// status → needs setup
	if code, m := a.do("GET", "/api/auth/status", nil); code != 200 || m["needsSetup"] != true {
		t.Fatalf("status: %d %v", code, m)
	}
	// setup logs us in
	if code, _ := a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"}); code != 200 {
		t.Fatalf("setup: %d", code)
	}
	code, me := a.do("GET", "/api/auth/me", nil)
	if code != 200 || me["role"] != "admin" {
		t.Fatalf("me: %d %v", code, me)
	}
	if secs, ok := me["sections"].([]any); !ok || len(secs) == 0 {
		t.Errorf("admin should see sections: %v", me["sections"])
	}

	// admin-only endpoints reachable
	if code, _ := a.do("GET", "/api/users", nil); code != 200 {
		t.Errorf("admin GET /users: %d", code)
	}
	if code, _ := a.do("PUT", "/api/settings", map[string]any{"disabledSections": []string{"events"}, "localhostNo2fa": false}); code != 200 {
		t.Errorf("admin PUT /settings: %d", code)
	}
	// store-backed CRUD: webhook + alert rule + parse rule
	if code, _ := a.do("POST", "/api/webhooks", map[string]string{"name": "wh", "url": "https://h"}); code != 200 {
		t.Errorf("create webhook: %d", code)
	}
	if code, _ := a.do("POST", "/api/alert-rules", map[string]any{"name": "r", "type": "state", "config": map[string]any{"events": []string{"die"}}, "enabled": true}); code != 200 {
		t.Errorf("create rule: %d", code)
	}
	if code, _ := a.do("POST", "/api/parse-rules", map[string]string{"name": "p", "pattern": "(?<x>.+)"}); code != 200 {
		t.Errorf("create parse rule: %d", code)
	}
	// smtp + ldap config round-trip (no secret leak)
	a.do("PUT", "/api/smtp", map[string]any{"host": "smtp", "port": 25, "from": "a@x", "to": "b@x", "password": "pw"})
	if _, m := a.do("GET", "/api/smtp", nil); m["hasPassword"] != true || m["password"] != nil {
		t.Errorf("smtp masked wrong: %v", m)
	}
}

func TestAPIDockerBackedReads(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})
	// Skip cleanly if there's no Docker daemon (handlers would 502).
	if code, _ := a.do("GET", "/api/system", nil); code != 200 {
		t.Skipf("docker daemon not available (GET /api/system → %d)", code)
	}
	for _, path := range []string{
		"/api/system", "/api/system/df", "/api/containers", "/api/images",
		"/api/volumes", "/api/networks", "/api/topology",
	} {
		if code, _ := a.do("GET", path, nil); code != 200 {
			t.Errorf("admin GET %s → %d (want 200)", path, code)
		}
	}
}

func TestAPIContainerDetailHandlers(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})
	if code, _ := a.do("GET", "/api/system", nil); code != 200 {
		t.Skipf("docker daemon not available (%d)", code)
	}
	ctx := context.Background()
	_ = a.dm.PullImage(ctx, 0, "alpine:latest", func(docker.PullProgress) {})
	id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{Image: "alpine:latest", Name: "dctest_api", Cmd: []string{"sleep", "300"}, Start: true})
	if err != nil {
		t.Skipf("cannot create container: %v", err)
	}
	t.Cleanup(func() {
		if cli, err := a.dm.Client(ctx, 0); err == nil {
			_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}
	})

	for _, path := range []string{
		"/api/containers/" + id,
		"/api/containers/" + id + "/diff",
		"/api/containers/" + id + "/top",
		"/api/containers/" + id + "/files?path=/",
		"/api/inspect/container?id=" + id,
		"/api/images/history?ref=alpine:latest",
		"/api/audit",
	} {
		if code, _ := a.do("GET", path, nil); code != 200 {
			t.Errorf("GET %s → %d (want 200)", path, code)
		}
	}
	// a lifecycle action over HTTP
	if code, _ := a.do("POST", "/api/containers/"+id+"/restart", nil); code != 200 {
		t.Errorf("POST restart → %d", code)
	}
	// the Prometheus exporter (outside the /api auth group)
	resp, err := a.c.Get(a.url + "/metrics")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("GET /metrics: %v (%v)", err, resp)
	} else {
		resp.Body.Close()
	}
}

func TestAPIDockerBackedWrites(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})
	if code, _ := a.do("GET", "/api/system", nil); code != 200 {
		t.Skipf("docker daemon not available (%d)", code)
	}
	ctx := context.Background()
	_ = a.dm.PullImage(ctx, 0, "alpine:latest", func(docker.PullProgress) {})

	// create container over HTTP
	_, m := a.do("POST", "/api/containers", map[string]any{
		"image": "alpine:latest", "name": "dctest_apiw", "cmd": []string{"sleep", "300"}, "start": true,
	})
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("create container did not return an id: %v", m)
	}
	t.Cleanup(func() {
		if cli, err := a.dm.Client(ctx, 0); err == nil {
			_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}
	})

	// rename + update resource limits
	if code, _ := a.do("POST", "/api/containers/"+id+"/rename", map[string]string{"name": "dctest_apiw2"}); code != 200 {
		t.Errorf("rename → %d", code)
	}
	if code, _ := a.do("POST", "/api/containers/"+id+"/update", map[string]any{"memory": 0, "nanoCpus": 0, "restartPolicy": "no"}); code != 200 {
		t.Errorf("update → %d", code)
	}

	// commit to a new image, then clean it up
	if code, mm := a.do("POST", "/api/containers/"+id+"/commit", map[string]string{"ref": "dctest_committed:latest", "comment": "test"}); code != 200 || mm["ok"] != true {
		t.Errorf("commit → %d %v", code, mm)
	}
	a.do("DELETE", "/api/images?ref=dctest_committed:latest&force=1", nil)

	// export filesystem (tar stream, body isn't JSON)
	if code, _ := a.do("GET", "/api/containers/"+id+"/export", nil); code != 200 {
		t.Errorf("export → %d", code)
	}

	// tag → remove → prune images
	if code, mm := a.do("POST", "/api/images/tag", map[string]string{"source": "alpine:latest", "target": "dctest_tag:latest"}); code != 200 || mm["ok"] != true {
		t.Errorf("tag → %d %v", code, mm)
	}
	if code, _ := a.do("DELETE", "/api/images?ref=dctest_tag:latest", nil); code != 200 {
		t.Errorf("remove image → %d", code)
	}
	if code, _ := a.do("POST", "/api/images/prune", nil); code != 200 {
		t.Errorf("prune images → %d", code)
	}

	// volumes: create → remove → prune
	if code, mm := a.do("POST", "/api/volumes", map[string]any{"name": "dctest_apivol"}); code != 200 || mm["ok"] != true {
		t.Errorf("create volume → %d %v", code, mm)
	}
	if code, _ := a.do("DELETE", "/api/volumes/dctest_apivol", nil); code != 200 {
		t.Errorf("remove volume → %d", code)
	}
	if code, _ := a.do("POST", "/api/volumes/prune", nil); code != 200 {
		t.Errorf("prune volumes → %d", code)
	}

	// save an image as a tar stream
	if resp, err := a.c.Get(a.url + "/api/images/save?ref=alpine:latest"); err != nil || resp.StatusCode != 200 {
		t.Errorf("save image: %v (%v)", err, resp)
	} else {
		resp.Body.Close()
	}

	// file round-trip: raw upload → download → delete
	up, err := a.c.Post(a.url+"/api/containers/"+id+"/files/upload?path=/tmp&name=up.txt",
		"application/octet-stream", bytes.NewReader([]byte("hello from test")))
	if err != nil || up.StatusCode != 200 {
		t.Errorf("upload file: %v (%v)", err, up)
	} else {
		up.Body.Close()
	}
	if code, _ := a.do("GET", "/api/containers/"+id+"/files/download?path=/tmp/up.txt", nil); code != 200 {
		t.Errorf("download file → %d", code)
	}
	if code, _ := a.do("DELETE", "/api/containers/"+id+"/files?path=/tmp/up.txt", nil); code != 200 {
		t.Errorf("delete file → %d", code)
	}

	// load an image from a saved tar (round-trips through docker load)
	if rc, err := a.dm.SaveImage(ctx, 0, []string{"alpine:latest"}); err == nil {
		data, _ := io.ReadAll(rc)
		rc.Close()
		if resp, err := a.c.Post(a.url+"/api/images/load", "application/x-tar", bytes.NewReader(data)); err != nil || resp.StatusCode != 200 {
			t.Errorf("load image: %v (%v)", err, resp)
		} else {
			resp.Body.Close()
		}
	}

	// import a filesystem tarball as a new image
	if rc, err := a.dm.ExportContainer(ctx, 0, id); err == nil {
		data, _ := io.ReadAll(rc)
		rc.Close()
		if resp, err := a.c.Post(a.url+"/api/images/import?ref=dctest/apiimport:latest", "application/x-tar", bytes.NewReader(data)); err != nil || resp.StatusCode != 200 {
			t.Errorf("import image: %v (%v)", err, resp)
		} else {
			resp.Body.Close()
		}
		a.do("DELETE", "/api/images?ref=dctest/apiimport:latest&force=1", nil)
	}

	// local host connectivity test (host id 1 = local)
	if code, mm := a.do("GET", "/api/hosts/1/test", nil); code != 200 || mm["ok"] != true {
		t.Errorf("local host test → %d %v", code, mm)
	}

	// registry test endpoint runs the docker login (bogus creds → ok:false, 200)
	a.do("POST", "/api/registries", map[string]string{"name": "r", "address": "docker.io", "username": "x", "secret": "y"})
	if code, _ := a.do("POST", "/api/registries/1/test", nil); code != 200 {
		t.Errorf("registry test → %d", code)
	}

	// remove a network created out-of-band
	if cli, err := a.dm.Client(ctx, 0); err == nil {
		if nw, err := cli.NetworkCreate(ctx, "dctest_apinet", network.CreateOptions{}); err == nil {
			if code, _ := a.do("DELETE", "/api/networks/"+nw.ID, nil); code != 200 {
				t.Errorf("remove network → %d", code)
			}
		}
	}
}

func TestAPIWebSockets(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})
	if code, _ := a.do("GET", "/api/system", nil); code != 200 {
		t.Skipf("docker daemon not available (%d)", code)
	}
	ctx := context.Background()
	_ = a.dm.PullImage(ctx, 0, "alpine:latest", func(docker.PullProgress) {})
	id, err := a.dm.CreateContainer(ctx, 0, docker.CreateSpec{Image: "alpine:latest", Name: "dctest_apiws", Cmd: []string{"sleep", "300"}, Start: true})
	if err != nil {
		t.Skipf("cannot create container: %v", err)
	}
	t.Cleanup(func() {
		if cli, err := a.dm.Client(ctx, 0); err == nil {
			_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}
	})

	// The websocket dial reuses the cookiejar client so the session cookie rides
	// along; http(s):// → ws(s)://.
	wsBase := "ws" + strings.TrimPrefix(a.url, "http")
	dialOpts := &websocket.DialOptions{HTTPClient: a.c}

	// 1) exec: send a command to stdin, read the echoed TTY output back.
	t.Run("exec", func(t *testing.T) {
		ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ectx, wsBase+"/api/containers/"+id+"/exec", dialOpts)
		if err != nil {
			t.Fatalf("dial exec: %v", err)
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)
		_ = conn.Write(ectx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":40}`))
		_ = conn.Write(ectx, websocket.MessageBinary, []byte("echo ws-exec-ok\n"))
		var seen bool
		for i := 0; i < 20 && !seen; i++ {
			_, data, err := conn.Read(ectx)
			if err != nil {
				break
			}
			if strings.Contains(string(data), "ws-exec-ok") {
				seen = true
			}
		}
		if !seen {
			t.Error("did not see exec output echoed back")
		}
	})

	// 2) events: trigger a container action and expect at least one event frame.
	t.Run("events", func(t *testing.T) {
		ectx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ectx, wsBase+"/api/events", dialOpts)
		if err != nil {
			t.Fatalf("dial events: %v", err)
		}
		defer conn.CloseNow()
		go func() {
			time.Sleep(400 * time.Millisecond)
			_ = a.dm.ContainerAction(ctx, 0, id, "kill")
		}()
		if _, _, err := conn.Read(ectx); err != nil {
			t.Errorf("expected an event frame: %v", err)
		}
	})

	// 4) the stats/logs hub bridge: a successful upgrade is enough to exercise
	// the handler (hub.Serve blocks until we disconnect).
	t.Run("hub", func(t *testing.T) {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(hctx, wsBase+"/api/ws", dialOpts)
		if err != nil {
			t.Fatalf("dial ws hub: %v", err)
		}
		conn.Close(websocket.StatusNormalClosure, "done")
	})

	// 3) pull: stream progress for an already-present image until "done".
	t.Run("pull", func(t *testing.T) {
		ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ectx, wsBase+"/api/images/pull?ref=alpine:latest", dialOpts)
		if err != nil {
			t.Fatalf("dial pull: %v", err)
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)
		var done bool
		for i := 0; i < 200 && !done; i++ {
			_, data, err := conn.Read(ectx)
			if err != nil {
				break
			}
			if strings.Contains(string(data), `"done":true`) {
				done = true
			}
		}
		if !done {
			t.Error("pull stream never reported done")
		}
	})
}

func TestAPIStoreBackedCRUD(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})

	// registries
	if code, m := a.do("POST", "/api/registries", map[string]string{"name": "hub", "address": "docker.io", "username": "u", "secret": "p"}); code != 200 {
		t.Errorf("create registry: %d %v", code, m)
	}
	if code, _ := a.do("GET", "/api/registries", nil); code != 200 {
		t.Errorf("list registries: %d", code)
	}
	if code, _ := a.do("DELETE", "/api/registries/1", nil); code != 200 {
		t.Errorf("delete registry: %d", code)
	}

	// hosts: list, create tcp, patch alert email, delete
	if code, _ := a.do("GET", "/api/hosts", nil); code != 200 {
		t.Errorf("list hosts: %d", code)
	}
	if code, _ := a.do("POST", "/api/hosts", map[string]string{"name": "h2", "kind": "tcp", "address": "tcp://127.0.0.1:2376"}); code != 200 {
		t.Errorf("create host: %d", code)
	}
	if code, _ := a.do("PATCH", "/api/hosts/2", map[string]string{"alertEmail": "ops@x.io"}); code != 200 {
		t.Errorf("patch host: %d", code)
	}
	if code, _ := a.do("DELETE", "/api/hosts/2", nil); code != 200 {
		t.Errorf("delete host: %d", code)
	}

	// parse rules
	a.do("POST", "/api/parse-rules", map[string]string{"name": "p", "pattern": "(?<x>.+)"})
	if code, _ := a.do("GET", "/api/parse-rules", nil); code != 200 {
		t.Errorf("list parse rules: %d", code)
	}
	a.do("DELETE", "/api/parse-rules/1", nil)

	// alert rule full lifecycle + feed/ack + webhook
	a.do("POST", "/api/webhooks", map[string]string{"name": "wh", "url": "https://h"})
	a.do("GET", "/api/webhooks", nil)
	a.do("POST", "/api/alert-rules", map[string]any{"name": "r", "type": "state", "config": map[string]any{"events": []string{"die"}}, "enabled": true})
	if code, _ := a.do("PUT", "/api/alert-rules/1", map[string]any{"name": "r2", "type": "state", "config": map[string]any{"events": []string{"die"}}, "severity": "critical"}); code != 200 {
		t.Errorf("update rule: %d", code)
	}
	a.do("PATCH", "/api/alert-rules/1", map[string]bool{"enabled": false})
	if code, _ := a.do("GET", "/api/alerts", nil); code != 200 {
		t.Errorf("alerts feed: %d", code)
	}
	a.do("DELETE", "/api/alert-rules/1", nil)
	a.do("DELETE", "/api/webhooks/1", nil)

	// LDAP + SMTP config (test endpoints return ok:false without a server, but
	// the handlers still run → 200).
	a.do("PUT", "/api/ldap", map[string]any{"enabled": true, "url": "ldap://127.0.0.1:1", "userBaseDn": "dc=x", "userFilter": "(uid=%s)"})
	if code, _ := a.do("GET", "/api/ldap", nil); code != 200 {
		t.Errorf("get ldap: %d", code)
	}
	if code, m := a.do("POST", "/api/ldap/test", map[string]any{}); code != 200 || m["ok"] != false {
		t.Errorf("ldap test should run and report failure: %d %v", code, m)
	}
	if code, m := a.do("POST", "/api/smtp/test", map[string]any{}); code != 200 || m["ok"] != false {
		t.Errorf("smtp test should run and report failure: %d %v", code, m)
	}
}

func TestAPIUserMgmtAndSettings(t *testing.T) {
	a := newAPI(t)
	_, _ = a.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})

	// settings + alert-rules listing
	if code, _ := a.do("GET", "/api/settings", nil); code != 200 {
		t.Errorf("get settings: %d", code)
	}
	if code, _ := a.do("GET", "/api/alert-rules", nil); code != 200 {
		t.Errorf("list alert-rules: %d", code)
	}

	// second user: create → update access → reset password → delete
	if code, _ := a.do("POST", "/api/users", map[string]any{"username": "bob", "password": "correcthorse123", "role": "user", "sections": []string{"containers"}}); code != 200 {
		t.Errorf("create user: %d", code)
	}
	if code, _ := a.do("PATCH", "/api/users/2", map[string]any{"role": "user", "readOnly": true, "sections": []string{"containers", "images"}}); code != 200 {
		t.Errorf("update user: %d", code)
	}
	if code, _ := a.do("POST", "/api/users/2/password", map[string]string{"password": "anotherstrongpw1"}); code != 200 {
		t.Errorf("reset password: %d", code)
	}
	if code, _ := a.do("DELETE", "/api/users/2", nil); code != 200 {
		t.Errorf("delete user: %d", code)
	}

	// demoting the last admin must be refused (ok:false, still 200).
	if code, m := a.do("PATCH", "/api/users/1", map[string]any{"role": "user"}); code != 200 || m["ok"] != false {
		t.Errorf("last-admin demotion should be refused: %d %v", code, m)
	}

	// metrics history (unknown container → empty series, 200).
	if code, _ := a.do("GET", "/api/metrics/history?container=nope&metric=cpu", nil); code != 200 {
		t.Errorf("metrics history: %d", code)
	}
	if code, _ := a.do("GET", "/api/metrics/history?container=nope&metric=bogus", nil); code != 400 {
		t.Errorf("unknown metric should be 400")
	}

	// TOTP enrollment over HTTP, then logout.
	_, enr := a.do("POST", "/api/auth/totp/setup", map[string]any{})
	secret, _ := enr["secret"].(string)
	if secret == "" {
		t.Fatalf("totp setup should return a secret: %v", enr)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if c, _ := a.do("POST", "/api/auth/totp/enable", map[string]string{"code": code}); c != 200 {
		t.Errorf("totp enable: %d", c)
	}
	if c, _ := a.do("POST", "/api/auth/logout", nil); c != 200 {
		t.Errorf("logout: %d", c)
	}

	// Full 2FA login over HTTP: password → MFA challenge → verify code.
	_, lr := a.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "correcthorse123"})
	if lr["mfaRequired"] != true {
		t.Fatalf("login should now require MFA: %v", lr)
	}
	mfaToken, _ := lr["mfaToken"].(string)
	code2, _ := totp.GenerateCode(secret, time.Now())
	if c, m := a.do("POST", "/api/auth/2fa", map[string]string{"mfaToken": mfaToken, "code": code2}); c != 200 || m["user"] == nil {
		t.Errorf("verify 2fa → %d %v", c, m)
	}
}

func TestAPIPermissionEnforcement(t *testing.T) {
	admin := newAPI(t)
	_, _ = admin.do("POST", "/api/auth/setup", map[string]string{"username": "admin", "password": "correcthorse123"})
	// allow localhost password-only login so the restricted user can sign in
	admin.do("PUT", "/api/settings", map[string]any{"disabledSections": []string{}, "localhostNo2fa": true})
	if code, _ := admin.do("POST", "/api/users", map[string]any{
		"username": "viewer", "password": "viewerpass123", "role": "user", "readOnly": true, "sections": []string{"containers"},
	}); code != 200 {
		t.Fatalf("create viewer: %d", code)
	}

	// the viewer uses its own cookie jar
	viewer := admin
	jar, _ := cookiejar.New(nil)
	viewer = &apiClient{t: t, c: &http.Client{Jar: jar}, url: admin.url}
	if code, _ := viewer.do("POST", "/api/auth/login", map[string]string{"username": "viewer", "password": "viewerpass123"}); code != 200 {
		t.Fatalf("viewer login: %d", code)
	}

	// allowed section (GET reaches the handler; no daemon, so 502 — not 403)
	if code, _ := viewer.do("GET", "/api/images", nil); code != 403 {
		t.Errorf("viewer GET /images should be 403 (no section), got %d", code)
	}
	if code, _ := viewer.do("GET", "/api/users", nil); code != 403 {
		t.Errorf("viewer GET /users should be 403 (admin only), got %d", code)
	}
	if code, _ := viewer.do("POST", "/api/containers/x/restart", nil); code != 403 {
		t.Errorf("read-only viewer POST should be 403, got %d", code)
	}
	if code, me := viewer.do("GET", "/api/auth/me", nil); code != 200 || me["readOnly"] != true {
		t.Errorf("viewer me: %d %v", code, me)
	}
}
