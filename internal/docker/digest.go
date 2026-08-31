package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Resolving what a mutable image tag currently points to on its registry,
// without pulling it. A tag comparison alone ("nginx:1.25" == "nginx:1.25")
// can't see that `nginx:1.25` was overwritten upstream since the last deploy —
// digest comparison can. Reuses the same registry v2 client, SSRF guards and
// Bearer-token handshake as registry_tags.go, and the same trust boundary:
// Docker Hub is reachable anonymously (it's public), any other host must be
// one the admin explicitly configured (AuthForHost), never an arbitrary host
// named in a ref.

// refTagOrDigest returns the tag or "sha256:..." digest portion of ref
// (without the leading '@'), or "latest" if neither is present.
func refTagOrDigest(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		return ref[i+1:]
	}
	rest := ref
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		rest = ref[slash+1:]
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		return rest[i+1:]
	}
	return "latest"
}

// ResolveImageDigest looks up the manifest digest an image reference currently
// resolves to on its registry. Returns "" (not an error) when the registry
// isn't reachable, isn't configured, or doesn't answer with a digest — this
// only ever informs a preview, so an unknown answer must never block one.
func (m *Manager) ResolveImageDigest(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(strings.ToLower(ref))
	if ref == "" {
		return "", nil
	}
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		return ref[i+1:], nil // already pinned to a digest — nothing to resolve
	}

	host := registryHost(ref)
	var auth *store.RegistryAuth
	if host != "docker.io" {
		a, err := m.store.AuthForHost(ctx, host)
		if err != nil {
			return "", nil // not a configured registry — no digest, matches ImageTags' rule
		}
		auth = a
	}

	repo, ok := repoPathForRef(ref, host)
	if !ok {
		return "", nil
	}
	pullHost := host
	if host == "docker.io" {
		pullHost = "registry-1.docker.io"
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo // Hub's v2 API needs the full path for official images
		}
	}
	return registryManifestDigest(ctx, pullHost, repo, refTagOrDigest(ref), auth)
}

// registryManifestDigest fetches a tag's manifest (or manifest list) and
// returns the registry-reported Docker-Content-Digest — the same digest
// `docker pull` would end up with, without downloading any layer.
func registryManifestDigest(ctx context.Context, host, repo, reference string, auth *store.RegistryAuth) (string, error) {
	manifestURL := registryScheme(host) + "://" + host + "/v2/" + repo + "/manifests/" + reference
	client := newRegistryClient(host)
	const accept = "application/vnd.docker.distribution.manifest.v2+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.oci.image.index.v1+json"

	get := func(bearer string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else if auth != nil && auth.Username != "" {
			req.SetBasicAuth(auth.Username, auth.Password)
		}
		return client.Do(req)
	}

	resp, err := get("")
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(challenge)), "bearer") {
			return "", nil // a Basic challenge we couldn't satisfy → no digest
		}
		token, err := fetchRegistryToken(ctx, client, challenge, repo, host, auth)
		if err != nil {
			return "", err
		}
		if resp, err = get(token); err != nil {
			return "", err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil // unknown tag — no digest, not an error
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry %s: %s", host, resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxRegistryRespBytes))
	return digest, nil
}

// RunningImageDigest resolves the digest a container's local image actually
// carries for ref's repository — what is really deployed right now, as
// opposed to what the tag currently resolves to on the registry
// (ResolveImageDigest). Returns "" when the image was never pulled from a
// registry (built locally, no RepoDigests): that means drift can't be judged
// for this service, not that something is wrong.
func (m *Manager) RunningImageDigest(ctx context.Context, hostID int64, containerID, ref string) (string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", err
	}
	c, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	img, _, err := cli.ImageInspectWithRaw(ctx, c.Image)
	if err != nil {
		return "", nil // image since removed/pruned — not worth surfacing as an error here
	}
	if len(img.RepoDigests) == 1 {
		if at := strings.LastIndexByte(img.RepoDigests[0], '@'); at >= 0 {
			return img.RepoDigests[0][at+1:], nil
		}
	}
	repo, ok := repoPathForRef(ref, registryHost(ref))
	if !ok {
		return "", nil
	}
	for _, rd := range img.RepoDigests {
		at := strings.LastIndexByte(rd, '@')
		if at < 0 {
			continue
		}
		if strings.HasSuffix(strings.ToLower(rd[:at]), "/"+repo) || strings.EqualFold(rd[:at], repo) {
			return rd[at+1:], nil
		}
	}
	return "", nil
}
