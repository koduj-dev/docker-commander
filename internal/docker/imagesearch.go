package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types/registry"
)

// Image-name autocomplete sources: a Docker Hub repository search proxied through
// the selected host's daemon (so no creds leave this process), plus a Docker Hub
// tag listing for a chosen repository. Both degrade to empty on any error — they
// only feed editor/form suggestions, never a hard dependency.

// ImageSearchResult is one Docker Hub search hit.
type ImageSearchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Stars       int    `json:"stars"`
	Official    bool   `json:"official"`
}

// hubClient is the outbound HTTP client for Docker Hub's public API (tag lists).
// Short timeout: autocomplete must stay snappy, and a slow/absent network just
// means no remote suggestions.
var hubClient = &http.Client{Timeout: 6 * time.Second}

// dockerRepo validates a Docker repository path (e.g. "library/nginx",
// "bitnami/redis"). It guards the value we interpolate into the Hub API URL so a
// crafted image ref can't escape the repository path.
var dockerRepo = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

// SearchImages proxies a Docker Hub repository search through the host daemon's
// /images/search. term is the partial name the user is typing.
func (m *Manager) SearchImages(ctx context.Context, hostID int64, term string, limit int) ([]ImageSearchResult, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return []ImageSearchResult{}, nil
	}
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 25
	}
	res, err := cli.ImageSearch(ctx, term, registry.SearchOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]ImageSearchResult, 0, len(res))
	for _, r := range res {
		out = append(out, ImageSearchResult{
			Name: r.Name, Description: r.Description, Stars: r.StarCount, Official: r.IsOfficial,
		})
	}
	return out, nil
}

// ImageTags lists tags for an image reference. Docker Hub repos use Hub's public
// API; for any other host, if the admin has configured it as a registry, tags
// come from that registry's v2 API (with credentials). Unconfigured hosts return
// no tags — so a ref can never make us contact an arbitrary host — and callers
// fall back to whatever the daemon already has pulled locally.
func (m *Manager) ImageTags(ctx context.Context, ref string) ([]string, error) {
	if host := registryHost(ref); host != "docker.io" {
		auth, err := m.store.AuthForHost(ctx, host)
		if err != nil {
			return []string{}, nil // not a configured registry — no remote tags
		}
		repo, ok := repoPathForRef(ref, host)
		if !ok {
			return []string{}, nil
		}
		return registryV2Tags(ctx, host, repo, auth)
	}
	return m.hubTags(ctx, ref)
}

// hubTags lists a Docker Hub repository's tags via Hub's public API.
func (m *Manager) hubTags(ctx context.Context, ref string) ([]string, error) {
	repo, ok := hubRepoPath(ref)
	if !ok {
		return []string{}, nil
	}
	u := "https://hub.docker.com/v2/repositories/" + repo + "/tags?page_size=100&ordering=last_updated"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hubClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return []string{}, nil // unknown repo — no suggestions, not an error
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker hub: %s", resp.Status)
	}
	var body struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(body.Results))
	for _, r := range body.Results {
		if r.Name != "" {
			tags = append(tags, r.Name)
		}
	}
	return tags, nil
}

// hubRepoPath maps an image reference to its Docker Hub repository path, or
// (",", false) if it isn't a Hub image. Official single-name images are expanded
// to library/<name>; any tag/digest suffix is dropped. The result is validated
// against dockerRepo before it can reach the Hub URL.
func hubRepoPath(ref string) (string, bool) {
	ref = strings.TrimSpace(strings.ToLower(ref))
	if ref == "" || registryHost(ref) != "docker.io" {
		return "", false
	}
	repo := strings.TrimPrefix(ref, "docker.io/")
	if i := strings.IndexByte(repo, '@'); i >= 0 {
		repo = repo[:i]
	}
	if i := strings.IndexByte(repo, ':'); i >= 0 {
		repo = repo[:i]
	}
	repo = strings.TrimPrefix(repo, "library/")
	if repo == "" {
		return "", false
	}
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	if !dockerRepo.MatchString(repo) {
		return "", false
	}
	return repo, true
}
