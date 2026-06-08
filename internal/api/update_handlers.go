package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateRepo     = "koduj-dev/docker-commander"
	updateCacheTTL = 6 * time.Hour
	updateTimeout  = 6 * time.Second
)

// updateStatus is what the UI consumes to decide whether to show the
// "update available" banner.
type updateStatus struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	URL             string `json:"url,omitempty"`
	PublishedAt     string `json:"publishedAt,omitempty"`
	Disabled        bool   `json:"disabled,omitempty"`
	Error           string `json:"error,omitempty"`
}

// updateChecker polls the GitHub Releases API at most once per updateCacheTTL
// and compares the latest tag with the running build version. It never blocks
// the binary's own work — the check is lazy, on request, behind a cache.
type updateChecker struct {
	current string
	enabled bool
	httpc   *http.Client

	mu     sync.Mutex
	cached updateStatus
	at     time.Time
	ok     bool
}

func newUpdateChecker(current string, enabled bool) *updateChecker {
	return &updateChecker{
		current: current,
		enabled: enabled,
		httpc:   &http.Client{Timeout: updateTimeout},
	}
}

// status returns the cached update status, refreshing from GitHub when the
// cache is empty or stale. On a transient fetch error it keeps the last good
// result (so the banner doesn't flap) and attaches the error.
func (u *updateChecker) status(ctx context.Context) updateStatus {
	if !u.enabled {
		return updateStatus{Current: u.current, Disabled: true}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.ok && time.Since(u.at) < updateCacheTTL {
		return u.cached
	}
	st := u.fetch(ctx)
	if st.Error == "" {
		u.cached, u.at, u.ok = st, time.Now(), true
		return st
	}
	if u.ok {
		prev := u.cached
		prev.Error = st.Error
		return prev
	}
	return st
}

func (u *updateChecker) fetch(ctx context.Context) updateStatus {
	out := updateStatus{Current: u.current}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "docker-commander/"+u.current)
	resp, err := u.httpc.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		out.Error = "github API: " + resp.Status
		return out
	}
	var rel struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		out.Error = err.Error()
		return out
	}
	out.Latest = strings.TrimPrefix(rel.TagName, "v")
	out.URL = rel.HTMLURL
	out.PublishedAt = rel.PublishedAt
	out.UpdateAvailable = semverLess(u.current, rel.TagName)
	return out
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.update.status(r.Context()))
}

// semverLess reports whether version a is strictly older than b. Versions are
// compared as dotted major.minor.patch (a leading "v" and any "-pre"/"+build"
// suffix are ignored). Unparseable versions (e.g. "dev") are treated as
// not-older, so a dev build never nags about updates.
func semverLess(a, b string) bool {
	av, aok := parseVer(a)
	bv, bok := parseVer(b)
	if !aok || !bok {
		return false
	}
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			return av[i] < bv[i]
		}
	}
	return false
}

func parseVer(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i := range parts {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
