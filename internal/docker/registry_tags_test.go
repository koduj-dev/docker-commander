package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// hostOf strips the scheme from an httptest server URL to get host[:port].
func hostOf(u string) string {
	return strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
}

func TestRepoPathForRef(t *testing.T) {
	cases := []struct {
		ref, host, want string
		ok              bool
	}{
		{"ghcr.io/owner/app:v1", "ghcr.io", "owner/app", true},
		{"ghcr.io/owner/app", "ghcr.io", "owner/app", true},
		{"ghcr.io/owner/app@sha256:abc", "ghcr.io", "owner/app", true},
		{"registry.example.com:5000/team/svc:dev", "registry.example.com:5000", "team/svc", true},
		{"ghcr.io/", "ghcr.io", "", false},
		{"ghcr.io/Bad__//x", "ghcr.io", "", false},
	}
	for _, c := range cases {
		got, ok := repoPathForRef(c.ref, c.host)
		if ok != c.ok || got != c.want {
			t.Errorf("repoPathForRef(%q,%q) = (%q,%v), want (%q,%v)", c.ref, c.host, got, ok, c.want, c.ok)
		}
	}
}

func TestRegistryV2Tags_BearerHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/owner/app/tags/list":
			if r.Header.Get("Authorization") != "Bearer tok123" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="reg",scope="repository:owner/app:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "owner/app", "tags": []string{"v1", "v2", "latest"}})
		case r.URL.Path == "/token":
			u, p, ok := r.BasicAuth()
			if !ok || u != "alice" || p != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.URL.Query().Get("scope"); got != "repository:owner/app:pull" {
				t.Errorf("token scope = %q, want repository:owner/app:pull", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok123"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tags, err := registryV2Tags(context.Background(), hostOf(srv.URL), "owner/app",
		&store.RegistryAuth{Username: "alice", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "v1,v2,latest" {
		t.Errorf("tags = %v, want [v1 v2 latest]", tags)
	}
}

func TestRegistryV2Tags_BasicAuthDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "bob" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.0", "1.1"}})
	}))
	defer srv.Close()
	tags, err := registryV2Tags(context.Background(), hostOf(srv.URL), "team/svc", &store.RegistryAuth{Username: "bob", Password: "pw"})
	if err != nil || strings.Join(tags, ",") != "1.0,1.1" {
		t.Fatalf("tags = %v, err = %v", tags, err)
	}
}

// PENTEST: a hostile registry's challenge must not redirect the token fetch to a
// non-https endpoint or an internal address (SSRF), including IPv6 / IPv4-mapped
// realms that a naive literal check would miss.
func TestFetchRegistryToken_RejectsInsecureAndInternalRealm(t *testing.T) {
	client := newRegistryClient("ghcr.io")
	cases := []struct {
		name, challenge string
	}{
		{"http downgrade", `Bearer realm="http://evil.example/token",service="x"`},
		{"internal ip (metadata)", `Bearer realm="https://169.254.169.254/latest/token",service="x"`},
		{"private ip", `Bearer realm="https://10.0.0.5/token",service="x"`},
		{"loopback v4", `Bearer realm="https://127.0.0.1/token",service="x"`},
		{"loopback v6", `Bearer realm="https://[::1]/token",service="x"`},
		{"private v6", `Bearer realm="https://[fd00::1]/token",service="x"`},
		{"v4-mapped metadata", `Bearer realm="https://[::ffff:169.254.169.254]/token",service="x"`},
		{"unspecified v6", `Bearer realm="https://[::]/token",service="x"`},
		{"no realm", `Bearer service="x"`},
		{"not a url", `Bearer realm="://nope",service="x"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Registry host is a public registry (NOT local), so the guards apply.
			_, err := fetchRegistryToken(context.Background(), client, c.challenge, "owner/app", "ghcr.io", nil)
			if err == nil {
				t.Errorf("expected the realm to be rejected for a public registry")
			}
		})
	}
}

// PENTEST: the guarded dialer blocks a connection to an internal IP literal when
// it isn't the configured registry host, but allows the registry host itself.
func TestGuardedDialer_BlocksInternalNonRegistry(t *testing.T) {
	dial := guardedDialer("registry.example.com")
	// Connecting to an internal IP that is NOT the registry must be refused
	// before any real connection (LookupIP on a literal does no DNS).
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:9"); err == nil {
		t.Error("dialer allowed an internal address that isn't the registry")
	}
	if _, err := dial(context.Background(), "tcp", "[::1]:9"); err == nil {
		t.Error("dialer allowed an internal IPv6 address that isn't the registry")
	}
}

// PENTEST: an unconfigured registry host yields no tags — ImageTags must not
// contact a host just because it appears in an image ref.
func TestImageTags_UnconfiguredHostNoRemoteTags(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := &Manager{store: st}
	tags, err := m.ImageTags(context.Background(), "ghcr.io/owner/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Errorf("unconfigured host should yield no remote tags, got %v", tags)
	}
}

// PENTEST: a Basic-only challenge we can't satisfy returns no tags (no token
// handshake attempted, no error escalation).
func TestRegistryV2Tags_BasicChallengeUnsatisfied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="reg"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	tags, err := registryV2Tags(context.Background(), hostOf(srv.URL), "owner/app", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected no tags for an unsatisfiable Basic challenge, got %v", tags)
	}
}
