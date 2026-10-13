package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/ws"
)

// Tests for serving the UI and API under a URL path prefix, as behind
// `location /dockercmd/ { proxy_pass … }` on a shared host.

// testIndex mirrors the shape of the Vite build: relative asset references.
const testIndex = `<!doctype html>
<html lang="en">
  <head>
    <script type="module" crossorigin src="./assets/app.js"></script>
  </head>
  <body><div id="root"></div></body>
</html>`

func newPrefixServer(t *testing.T, cfg config.Config) http.Handler {
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
	webFS := fstest.MapFS{
		"index.html":    {Data: []byte(testIndex)},
		"assets/app.js": {Data: []byte("console.log('app')")},
	}
	srv := NewServer(cfg, st, auth.NewService(st, tm), auth.NewMiddleware(tm, st),
		dm, ws.NewHub(dm), monitor.New(st, dm, nil), history.Open(context.Background(), history.Config{}), webFS)
	return srv.Handler()
}

func serve(h http.Handler, method, target, prefixHeader string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	if prefixHeader != "" {
		req.Header.Set("X-Forwarded-Prefix", prefixHeader)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// isAuthStatus reports whether the response came from the API route rather
// than the SPA fallback (which answers every unknown path with 200 too).
func isAuthStatus(w *httptest.ResponseRecorder) bool {
	var m map[string]any
	return w.Code == http.StatusOK && json.Unmarshal(w.Body.Bytes(), &m) == nil && m["needsSetup"] != nil
}

func baseHref(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	body := w.Body.String()
	if !strings.Contains(body, `<div id="root">`) {
		t.Fatalf("not the SPA index (%d): %s", w.Code, body)
	}
	_, rest, ok := strings.Cut(body, `<base href="`)
	if !ok {
		t.Fatalf("index has no <base href>: %s", body)
	}
	href, _, _ := strings.Cut(rest, `"`)
	return href
}

func TestPathPrefixRouting(t *testing.T) {
	fwd := newPrefixServer(t, config.Config{ForwardedPrefix: true})
	base := newPrefixServer(t, config.Config{BasePath: "/dockercmd"})
	both := newPrefixServer(t, config.Config{BasePath: "/configured", ForwardedPrefix: true})

	cases := []struct {
		name   string
		h      http.Handler
		target string
		header string
	}{
		{"header, proxy strips the prefix", fwd, "/api/auth/status", "/dockercmd"},
		{"header, proxy keeps the prefix", fwd, "/dockercmd/api/auth/status", "/dockercmd"},
		{"header with trailing slash", fwd, "/dockercmd/api/auth/status", "/dockercmd/"},
		{"nested header prefix", fwd, "/tools/dockercmd/api/auth/status", "/tools/dockercmd"},
		{"no header, served at the root", fwd, "/api/auth/status", ""},
		{"base path, prefixed request", base, "/dockercmd/api/auth/status", ""},
		{"base path, stripped request", base, "/api/auth/status", ""},
		{"header overrides base path", both, "/dockercmd/api/auth/status", "/dockercmd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := serve(tc.h, "GET", tc.target, tc.header, nil); !isAuthStatus(w) {
				t.Errorf("GET %s (prefix %q): want the API, got %d %s", tc.target, tc.header, w.Code, w.Body)
			}
		})
	}
}

func TestPathPrefixIndexAndAssets(t *testing.T) {
	fwd := newPrefixServer(t, config.Config{ForwardedPrefix: true})
	base := newPrefixServer(t, config.Config{BasePath: "/dockercmd"})

	for _, tc := range []struct {
		name, target, header, want string
		h                          http.Handler
	}{
		{"root, no prefix", "/", "", "/", fwd},
		{"deep link, no prefix", "/containers/abc", "", "/", fwd},
		{"header, root", "/", "/dockercmd", "/dockercmd/", fwd},
		{"header, prefixed deep link", "/dockercmd/containers/abc", "/dockercmd", "/dockercmd/", fwd},
		{"header, stripped deep link", "/containers/abc", "/dockercmd", "/dockercmd/", fwd},
		{"header, prefix without slash", "/dockercmd", "/dockercmd", "/dockercmd/", fwd},
		{"base path, root", "/dockercmd/", "", "/dockercmd/", base},
		{"base path, explicit index.html", "/dockercmd/index.html", "", "/dockercmd/", base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(tc.h, "GET", tc.target, tc.header, nil)
			if got := baseHref(t, w); got != tc.want {
				t.Errorf("base href = %q, want %q", got, tc.want)
			}
			if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("index Cache-Control = %q, want no-store (the page differs per prefix)", cc)
			}
			if v := w.Header().Values("Vary"); !strings.Contains(strings.Join(v, ","), "X-Forwarded-Prefix") {
				t.Errorf("index Vary = %v, want X-Forwarded-Prefix", v)
			}
		})
	}

	for _, target := range []string{"/dockercmd/assets/app.js", "/assets/app.js"} {
		w := serve(fwd, "GET", target, "/dockercmd", nil)
		if w.Code != http.StatusOK || w.Body.String() != "console.log('app')" {
			t.Errorf("asset %s under the prefix: %d %q", target, w.Code, w.Body)
		}
	}
}

// The MCP transport is under the prefix as well; an unauthenticated call gets
// its bearer challenge, not the SPA.
func TestPathPrefixMCP(t *testing.T) {
	h := newPrefixServer(t, config.Config{ForwardedPrefix: true, MCPEnabled: true})
	w := serve(h, "POST", "/dockercmd/mcp", "/dockercmd", strings.NewReader(`{}`))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /dockercmd/mcp: %d %s, want 401", w.Code, w.Body)
	}
}

// With DC_FORWARDED_PREFIX=0 the header must change nothing at all.
func TestPathPrefixHeaderCanBeDisabled(t *testing.T) {
	h := newPrefixServer(t, config.Config{ForwardedPrefix: false})
	if got := baseHref(t, serve(h, "GET", "/", "/dockercmd", nil)); got != "/" {
		t.Errorf("disabled header still set base href %q", got)
	}
	if w := serve(h, "GET", "/dockercmd/api/auth/status", "/dockercmd", nil); isAuthStatus(w) {
		t.Error("disabled header still stripped the prefix from the path")
	}
	// The configured base path still applies.
	h = newPrefixServer(t, config.Config{BasePath: "/configured", ForwardedPrefix: false})
	if got := baseHref(t, serve(h, "GET", "/", "/dockercmd", nil)); got != "/configured/" {
		t.Errorf("disabled header: base href %q, want the configured /configured/", got)
	}
}

// The prefix is matched as whole path segments only: with base path /a, an
// unprefixed /api/… is the API, not "/a" + "pi/…".
func TestPathPrefixMatchesWholeSegments(t *testing.T) {
	h := newPrefixServer(t, config.Config{BasePath: "/a"})
	if w := serve(h, "GET", "/api/auth/status", "", nil); !isAuthStatus(w) {
		t.Errorf("base path /a cut into /api: %d %s", w.Code, w.Body)
	}
	if w := serve(h, "GET", "/a/api/auth/status", "", nil); !isAuthStatus(w) {
		t.Errorf("base path /a not stripped: %d %s", w.Code, w.Body)
	}
}

// Encoded characters later in the path survive stripping (chi routes on
// RawPath when it is set).
func TestPathPrefixKeepsRawPath(t *testing.T) {
	req := httptest.NewRequest("GET", "/dockercmd/api/volumes/a%2Fb", nil)
	got := stripPathPrefix(req, "/dockercmd")
	if got.URL.Path != "/api/volumes/a/b" || got.URL.RawPath != "/api/volumes/a%2Fb" {
		t.Errorf("stripped to Path %q RawPath %q", got.URL.Path, got.URL.RawPath)
	}
	if req.URL.Path != "/dockercmd/api/volumes/a/b" {
		t.Errorf("the original request was modified: %q", req.URL.Path)
	}
}

// The session cookie is scoped to the prefix: on a shared host every other
// app under the same origin would otherwise receive the session token.
func TestPathPrefixScopesSessionCookie(t *testing.T) {
	h := newPrefixServer(t, config.Config{ForwardedPrefix: true})
	w := serve(h, "POST", "/api/auth/setup", "/dockercmd",
		strings.NewReader(`{"username":"admin","password":"correcthorse123"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	set := cookieNamed(w.Result().Cookies(), auth.SessionCookie)
	if set == nil || set.Path != "/dockercmd/" {
		t.Fatalf("session cookie %+v, want Path=/dockercmd/", set)
	}

	// Logout must clear the cookie at the same path, or the browser keeps it.
	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	req.Header.Set("X-Forwarded-Prefix", "/dockercmd")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: set.Value})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	cleared := cookieNamed(w.Result().Cookies(), auth.SessionCookie)
	if cleared == nil || cleared.Path != "/dockercmd/" || cleared.MaxAge >= 0 {
		t.Errorf("logout cookie %+v, want an expired one at Path=/dockercmd/", cleared)
	}

	// Served at the root, the cookie stays at "/".
	h = newPrefixServer(t, config.Config{ForwardedPrefix: true})
	w = serve(h, "POST", "/api/auth/setup", "",
		strings.NewReader(`{"username":"admin","password":"correcthorse123"}`))
	if c := cookieNamed(w.Result().Cookies(), auth.SessionCookie); c == nil || c.Path != "/" {
		t.Errorf("root session cookie %+v, want Path=/", c)
	}
}

func cookieNamed(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// The OAuth consent form posts back under the prefix, or the decision would
// land on the host's root, outside Docker Commander.
func TestPathPrefixOAuthConsentAction(t *testing.T) {
	s, _, cookie, _ := newAS(t)
	s.cfg.ForwardedPrefix = true
	cid := registerClient(t, s, penRedirect)
	_, challenge := pkcePair(t)
	q := url.Values{
		"client_id": {cid}, "redirect_uri": {penRedirect}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil)
	req.Header.Set("X-Forwarded-Prefix", "/dockercmd")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.pathPrefix(http.HandlerFunc(s.handleOAuthAuthorize)).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `action="/dockercmd/oauth/authorize"`) {
		t.Errorf("consent form does not post under the prefix: %s", w.Body)
	}
}

// --- pentests: a hostile X-Forwarded-Prefix ---------------------------------

// The header is believed from any peer, so every value that is not a plain
// path must be dropped whole: the page falls back to the root, as if the
// header were absent. Asserting the exact href (not just "no <script>") is
// what proves the value was refused rather than escaped into something else.
func TestPentestForwardedPrefixInjection(t *testing.T) {
	h := newPrefixServer(t, config.Config{ForwardedPrefix: true})
	for _, evil := range []string{
		"//evil.example",
		"https://evil.example",
		`/"><script>alert(1)</script>`,
		"/x' onload='alert(1)",
		"javascript:alert(1)",
		"/a/../../etc",
		"/a%2f..",
		`\\evil.example`,
		"/" + strings.Repeat("a", 300),
	} {
		w := serve(h, "GET", "/", evil, nil)
		if got := baseHref(t, w); got != "/" {
			t.Errorf("prefix %q produced base href %q, want it ignored (/)", evil, got)
		}
		if strings.Contains(w.Body.String(), "evil.example") || strings.Contains(w.Body.String(), "alert(1)") {
			t.Errorf("prefix %q leaked into the page: %s", evil, w.Body)
		}
	}
}

// A refused header must not leave its value in the session cookie path either.
func TestPentestForwardedPrefixCookiePath(t *testing.T) {
	h := newPrefixServer(t, config.Config{ForwardedPrefix: true})
	w := serve(h, "POST", "/api/auth/setup", "/a;Domain=evil.example",
		strings.NewReader(`{"username":"admin","password":"correcthorse123"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	for _, raw := range w.Header().Values("Set-Cookie") {
		if strings.Contains(raw, "evil.example") {
			t.Errorf("refused prefix reached Set-Cookie: %s", raw)
		}
	}
	if c := cookieNamed(w.Result().Cookies(), auth.SessionCookie); c == nil || c.Path != "/" {
		t.Errorf("session cookie %+v, want Path=/", c)
	}
}

// Stripping a prefix must not reach anything the stripped path could not:
// the API behind it is still behind the session check.
func TestPentestForwardedPrefixNoAuthBypass(t *testing.T) {
	h := newPrefixServer(t, config.Config{ForwardedPrefix: true})
	for _, target := range []string{"/dockercmd/api/auth/me", "/dockercmd/api/users", "/api/users"} {
		if w := serve(h, "GET", target, "/dockercmd", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with prefix: %d, want 401", target, w.Code)
		}
	}
}
