package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/koduj-dev/docker-commander/internal/config"
)

// OAuth discovery for MCP connectors when Docker Commander is published under a
// path (DC_MCP_PUBLIC_URL=https://host/dockercmd). The discovery itself is run
// by the MCP SDK's own client code, against an nginx stand-in, so the tests
// follow the URLs real clients try rather than a list of our own.

// nginxFront stands in for a shared host: location /dockercmd/ proxies to
// Docker Commander with X-Forwarded-Prefix, and with rootLocations also the two
// metadata paths at the host root. Everything else is another site's 404. It
// records every path asked for and the status it got.
type nginxFront struct {
	srv *httptest.Server
	mu  sync.Mutex
	log []string
}

func newNginxFront(t *testing.T, rootLocations bool, mkDC func(publicURL string) http.Handler) *nginxFront {
	t.Helper()
	f := &nginxFront{}
	var dc http.Handler
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		switch {
		case strings.HasPrefix(r.URL.Path, "/dockercmd/"):
			r.Header.Set("X-Forwarded-Prefix", "/dockercmd")
			dc.ServeHTTP(rec, r)
		case rootLocations && (r.URL.Path == "/.well-known/oauth-authorization-server/dockercmd" ||
			r.URL.Path == "/.well-known/oauth-protected-resource/dockercmd/mcp"):
			dc.ServeHTTP(rec, r)
		default:
			http.NotFound(rec, r)
		}
		f.mu.Lock()
		f.log = append(f.log, r.Method+" "+r.URL.Path+" "+http.StatusText(rec.Code))
		f.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(f.srv.Close)
	dc = mkDC(f.srv.URL + "/dockercmd")
	return f
}

func (f *nginxFront) asked(entry string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.log, entry)
}

func mcpUnderPrefix(t *testing.T) func(string) http.Handler {
	return func(publicURL string) http.Handler {
		return newPrefixServer(t, config.Config{ForwardedPrefix: true, MCPEnabled: true, MCPPublicURL: publicURL})
	}
}

func TestPathPrefixOAuthDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name          string
		rootLocations bool
		servedBy      string // the discovery URL that must have answered
	}{
		{"root locations", true, "GET /.well-known/oauth-authorization-server/dockercmd OK"},
		{"prefix only", false, "GET /dockercmd/.well-known/openid-configuration OK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNginxFront(t, tc.rootLocations, mcpUnderPrefix(t))
			issuer := f.srv.URL + "/dockercmd"
			ctx, c := t.Context(), f.srv.Client()

			asm, err := mcpauth.GetAuthServerMetadata(ctx, issuer, c)
			if err != nil || asm == nil {
				t.Fatalf("authorization server discovery: %v, %v (asked: %v)", asm, err, f.log)
			}
			if !f.asked(tc.servedBy) {
				t.Errorf("metadata not served by %q; asked: %v", tc.servedBy, f.log)
			}
			if asm.Issuer != issuer || asm.AuthorizationEndpoint != issuer+"/oauth/authorize" ||
				asm.TokenEndpoint != issuer+"/oauth/token" || asm.RegistrationEndpoint != issuer+"/oauth/register" {
				t.Errorf("metadata leaves the prefix: %+v", asm)
			}

			// Protected-resource metadata, the way a client finds it: from
			// the 401 challenge of the MCP endpoint.
			resource := issuer + "/mcp"
			resp, err := c.Post(resource, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			chs, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
			if err != nil || len(chs) == 0 {
				t.Fatalf("401 challenge: %v %v", chs, err)
			}
			metaURL := chs[0].Params["resource_metadata"]
			if !strings.HasPrefix(metaURL, issuer+"/") {
				t.Fatalf("resource_metadata %q is outside the prefix", metaURL)
			}
			prm, err := oauthex.GetProtectedResourceMetadata(ctx, metaURL, resource, c)
			if err != nil || !slices.Contains(prm.AuthorizationServers, issuer) {
				t.Errorf("protected-resource metadata: %+v, %v", prm, err)
			}
			if tc.rootLocations {
				// RFC 9728 path insertion, which clients try without a challenge.
				inserted := f.srv.URL + "/.well-known/oauth-protected-resource/dockercmd/mcp"
				if _, err := oauthex.GetProtectedResourceMetadata(ctx, inserted, resource, c); err != nil {
					t.Errorf("path-inserted protected-resource metadata: %v", err)
				}
			}
		})
	}
}

// The OIDC document is checked by clients against OIDC's required fields (the
// MCP TypeScript SDK refuses it otherwise), so each of them is asserted here.
// It must not claim OIDC itself: no "openid" scope, no keys.
func TestPathPrefixOIDCDiscoveryDocument(t *testing.T) {
	f := newNginxFront(t, false, mcpUnderPrefix(t))
	issuer := f.srv.URL + "/dockercmd"
	get := func(u string) map[string]any {
		t.Helper()
		resp, err := f.srv.Client().Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&m) != nil {
			t.Fatalf("GET %s: %d, not JSON", u, resp.StatusCode)
		}
		return m
	}
	m := get(issuer + "/.well-known/openid-configuration")
	if m["issuer"] != issuer || m["jwks_uri"] != issuer+"/oauth/jwks" {
		t.Errorf("issuer/jwks_uri: %v %v", m["issuer"], m["jwks_uri"])
	}
	for _, k := range []string{"response_types_supported", "subject_types_supported",
		"id_token_signing_alg_values_supported", "code_challenge_methods_supported"} {
		if v, ok := m[k].([]any); !ok || len(v) == 0 {
			t.Errorf("%s missing or empty: %v", k, m[k])
		}
	}
	if scopes, _ := m["scopes_supported"].([]any); slices.Contains(scopes, any("openid")) {
		t.Errorf("claims the openid scope: %v", scopes)
	}
	if keys, ok := get(issuer + "/oauth/jwks")["keys"].([]any); !ok || len(keys) != 0 {
		t.Errorf("jwks: want an empty key set, got %v", keys)
	}
}

// At the root of its own host nothing new is served: the existing discovery
// paths cover it, and the OIDC document would be one more surface for nothing.
func TestPathPrefixOAuthRoutesOnlyUnderPath(t *testing.T) {
	for _, publicURL := range []string{"https://dc.example", "https://dc.example/", ""} {
		h := newPrefixServer(t, config.Config{MCPEnabled: true, MCPPublicURL: publicURL})
		for _, p := range []string{"/.well-known/openid-configuration", "/oauth/jwks"} {
			w := serve(h, "GET", p, "", nil)
			if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Errorf("public URL %q: %s is served: %d %s", publicURL, p, w.Code, w.Body)
			}
		}
	}
}

// A public URL whose path is /mcp collides with the resource-suffixed discovery
// path the server always registers; mounting must not panic.
func TestPathPrefixOAuthPublicPathMCP(t *testing.T) {
	h := newPrefixServer(t, config.Config{MCPEnabled: true, MCPPublicURL: "https://dc.example/mcp"})
	w := serve(h, "GET", "/.well-known/oauth-authorization-server/mcp", "", nil)
	var m map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &m) != nil || m["issuer"] != "https://dc.example/mcp" {
		t.Errorf("metadata: %d %s", w.Code, w.Body)
	}
}

// --- pentests ----------------------------------------------------------------

// The issuer comes from DC_MCP_PUBLIC_URL alone. A client that sends its own
// X-Forwarded-Prefix reaches the discovery documents under that prefix, but
// they must still name the configured issuer and endpoints: metadata pointing
// elsewhere would send the next client's authorization code there.
func TestPentestForwardedPrefixOAuthIssuer(t *testing.T) {
	const public = "https://host.example/dockercmd"
	h := newPrefixServer(t, config.Config{ForwardedPrefix: true, MCPEnabled: true, MCPPublicURL: public})
	for _, target := range []string{
		"/evil/.well-known/openid-configuration",
		"/evil/.well-known/oauth-authorization-server",
	} {
		w := serve(h, "GET", target, "/evil", nil)
		var m map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &m) != nil {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
		for _, k := range []string{"issuer", "authorization_endpoint", "token_endpoint", "registration_endpoint"} {
			if v, _ := m[k].(string); !strings.HasPrefix(v, public) {
				t.Errorf("%s: %s = %q, not under the configured %s", target, k, v, public)
			}
		}
	}
}

// Only the issuer's own path-inserted URL is metadata; another path under the
// well-known prefix must not answer as if it were this server's issuer.
func TestPentestOAuthPathInsertionExactMatch(t *testing.T) {
	h := newPrefixServer(t, config.Config{MCPEnabled: true, MCPPublicURL: "https://host.example/dockercmd"})
	for _, p := range []string{
		"/.well-known/oauth-authorization-server/other",
		"/.well-known/oauth-authorization-server/dockercmd/x",
		"/.well-known/oauth-authorization-server/dockercmdx",
		"/.well-known/oauth-protected-resource/other/mcp",
	} {
		w := serve(h, "GET", p, "", nil)
		if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s answered as metadata: %d %s", p, w.Code, w.Body)
		}
	}
}
