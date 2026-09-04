package docker

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestRefTagOrDigest(t *testing.T) {
	cases := []struct{ ref, want string }{
		{"nginx", "latest"},
		{"nginx:1.25", "1.25"},
		{"ghcr.io/owner/app:v2", "v2"},
		{"registry.example.com:5000/team/svc:dev", "dev"},
		{"nginx@sha256:abc123", "sha256:abc123"},
	}
	for _, c := range cases {
		if got := refTagOrDigest(c.ref); got != c.want {
			t.Errorf("refTagOrDigest(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestRegistryManifestDigest_BearerHandshake(t *testing.T) {
	const wantDigest = "sha256:deadbeef00000000000000000000000000000000000000000000000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/owner/app/manifests/v1":
			if r.Header.Get("Authorization") != "Bearer tok123" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="reg",scope="repository:owner/app:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", wantDigest)
			w.Write([]byte(`{}`))
		case r.URL.Path == "/token":
			w.Write([]byte(`{"token":"tok123"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	digest, err := registryManifestDigest(context.Background(), hostOf(srv.URL), "owner/app", "v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if digest != wantDigest {
		t.Errorf("digest = %q, want %q", digest, wantDigest)
	}
}

// A registry answering 200 without the Docker-Content-Digest header (some
// third-party registries omit it) must yield "", not a guessed value.
func TestRegistryManifestDigest_NoHeaderYieldsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	digest, err := registryManifestDigest(context.Background(), hostOf(srv.URL), "owner/app", "v1", nil)
	if err != nil || digest != "" {
		t.Errorf("digest = %q, err = %v, want empty/no error", digest, err)
	}
}

func TestRegistryManifestDigest_UnknownTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	digest, err := registryManifestDigest(context.Background(), hostOf(srv.URL), "owner/app", "missing", nil)
	if err != nil || digest != "" {
		t.Errorf("digest = %q, err = %v, want empty/no error for an unknown tag", digest, err)
	}
}

// PENTEST: an unconfigured registry host yields no digest — mirrors
// TestImageTags_UnconfiguredHostNoRemoteTags. An arbitrary ref must never make
// ResolveImageDigest contact a host the admin hasn't explicitly configured.
func TestResolveImageDigest_UnconfiguredHostNoDigest(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := &Manager{store: st}
	digest, err := m.ResolveImageDigest(context.Background(), "ghcr.io/owner/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	if digest != "" {
		t.Errorf("expected no digest for an unconfigured host, got %q", digest)
	}
}

// A ref already pinned to a digest resolves to itself without any network call.
func TestResolveImageDigest_AlreadyPinned(t *testing.T) {
	m := &Manager{}
	digest, err := m.ResolveImageDigest(context.Background(), "ghcr.io/owner/app@sha256:cafe")
	if err != nil || digest != "sha256:cafe" {
		t.Errorf("digest = %q, err = %v, want sha256:cafe/nil", digest, err)
	}
}

// End-to-end through the configured-registry path: a registry the admin has
// added (CreateRegistry) is one ResolveImageDigest is allowed to contact, and
// the Bearer handshake + Docker-Content-Digest header round-trip correctly.
func TestResolveImageDigest_ConfiguredRegistry(t *testing.T) {
	const wantDigest = "sha256:cafebabe0000000000000000000000000000000000000000000000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/team/svc/manifests/dev" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", wantDigest)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(c)
	host := hostOf(srv.URL)
	if _, err := st.CreateRegistry(context.Background(), "test", host, "", ""); err != nil {
		t.Fatal(err)
	}

	m := &Manager{store: st}
	digest, err := m.ResolveImageDigest(context.Background(), host+"/team/svc:dev")
	if err != nil {
		t.Fatal(err)
	}
	if digest != wantDigest {
		t.Errorf("digest = %q, want %q", digest, wantDigest)
	}
}

// PENTEST/regression for P2-3: a tag's case must reach the registry
// unchanged. Tags are case-sensitive per the distribution spec, unlike the
// registry host and repository path — lowercasing "RC1" to "rc1" before the
// request would silently query a different (or nonexistent) tag.
func TestResolveImageDigest_PreservesTagCase(t *testing.T) {
	const wantDigest = "sha256:cafef00d0000000000000000000000000000000000000000000000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/team/svc/manifests/RC1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", wantDigest)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(c)
	host := hostOf(srv.URL)
	if _, err := st.CreateRegistry(context.Background(), "test", host, "", ""); err != nil {
		t.Fatal(err)
	}

	m := &Manager{store: st}
	digest, err := m.ResolveImageDigest(context.Background(), host+"/team/svc:RC1")
	if err != nil {
		t.Fatal(err)
	}
	if digest != wantDigest {
		t.Errorf("digest = %q, want %q (server only answers the exact-case tag RC1)", digest, wantDigest)
	}
}

// "RC1" and "rc1" are two different tags, not the same tag queried twice —
// each must resolve to its own registry-reported digest.
func TestResolveImageDigest_DifferentCaseTagsAreDistinct(t *testing.T) {
	digestUpper := "sha256:1111111111111111111111111111111111111111111111111111111111aa"
	digestLower := "sha256:2222222222222222222222222222222222222222222222222222222222bb"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/svc/manifests/RC1":
			w.Header().Set("Docker-Content-Digest", digestUpper)
			w.Write([]byte(`{}`))
		case "/v2/team/svc/manifests/rc1":
			w.Header().Set("Docker-Content-Digest", digestLower)
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(c)
	host := hostOf(srv.URL)
	if _, err := st.CreateRegistry(context.Background(), "test", host, "", ""); err != nil {
		t.Fatal(err)
	}

	m := &Manager{store: st}
	gotUpper, err := m.ResolveImageDigest(context.Background(), host+"/team/svc:RC1")
	if err != nil {
		t.Fatal(err)
	}
	gotLower, err := m.ResolveImageDigest(context.Background(), host+"/team/svc:rc1")
	if err != nil {
		t.Fatal(err)
	}
	if gotUpper != digestUpper {
		t.Errorf("RC1 digest = %q, want %q", gotUpper, digestUpper)
	}
	if gotLower != digestLower {
		t.Errorf("rc1 digest = %q, want %q", gotLower, digestLower)
	}
	if gotUpper == gotLower {
		t.Errorf("RC1 and rc1 resolved to the same digest %q — tag case was collapsed", gotUpper)
	}
}
