package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/koduj-dev/docker-commander/internal/version"
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
	out.UpdateAvailable = version.Less(u.current, rel.TagName)
	return out
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.update.status(r.Context()))
}
