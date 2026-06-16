package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/koduj-dev/docker-commander/internal/selfupdate"
	"github.com/koduj-dev/docker-commander/internal/version"
)

// errSelfUpdateDisabled is returned by apply when web-triggered self-update is
// turned off (DC_SELF_UPDATE=0). errUpdateInProgress means another apply holds
// the lock.
var (
	errSelfUpdateDisabled = errors.New("self-update is disabled")
	errUpdateInProgress   = errors.New("an update is already in progress")
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
	// SelfUpdate reports whether the in-app "Update & restart" action is allowed
	// (DC_SELF_UPDATE) AND the running process can restart itself — i.e. whether
	// the UI should offer the one-tap button.
	SelfUpdate bool `json:"selfUpdate"`
}

// updateChecker polls the GitHub Releases API at most once per updateCacheTTL
// and compares the latest tag with the running build version. It never blocks
// the binary's own work — the check is lazy, on request, behind a cache.
type updateChecker struct {
	current    string
	enabled    bool // periodic check / banner
	selfUpdate bool // web-triggered apply allowed (DC_SELF_UPDATE)
	httpc      *http.Client

	mu     sync.Mutex
	cached updateStatus
	at     time.Time
	ok     bool

	applyMu sync.Mutex // serialises apply so two upgrades can't race
}

func newUpdateChecker(current string, enabled, selfUpdate bool) *updateChecker {
	return &updateChecker{
		current:    current,
		enabled:    enabled,
		selfUpdate: selfUpdate,
		httpc:      &http.Client{Timeout: updateTimeout},
	}
}

// invalidate drops the cached status so the next status() re-fetches (used after
// a successful apply, though the process restarts immediately after anyway).
func (u *updateChecker) invalidate() {
	u.mu.Lock()
	u.ok = false
	u.mu.Unlock()
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

// apply downloads, verifies and installs the latest release, replacing the
// running binary on disk. It is gated by both DC_UPDATE_CHECK (outbound calls)
// and DC_SELF_UPDATE (web-triggered swap), and serialised so two upgrades can't
// race. The new binary takes effect on the next restart.
func (u *updateChecker) apply(ctx context.Context) (selfupdate.Result, error) {
	if !u.enabled || !u.selfUpdate {
		return selfupdate.Result{}, errSelfUpdateDisabled
	}
	if !u.applyMu.TryLock() {
		return selfupdate.Result{}, errUpdateInProgress
	}
	defer u.applyMu.Unlock()

	res, err := selfupdate.Apply(ctx, u.current)
	if err == nil {
		u.invalidate()
	}
	return res, err
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	st := s.update.status(r.Context())
	// The one-tap button is offered only when self-update is allowed and the
	// process can actually restart itself (wired by main; nil e.g. on Windows).
	st.SelfUpdate = s.update.enabled && s.update.selfUpdate && s.onRestart != nil
	writeJSON(w, http.StatusOK, st)
}

// handleApplyUpdate performs the verified download + binary swap. Admin-gated by
// the router (the "update" section maps to __admin).
func (s *Server) handleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	// Detach from the request context so closing the browser tab can't abort a
	// download mid-flight; Apply enforces its own timeout.
	res, err := s.update.apply(context.WithoutCancel(r.Context()))
	switch {
	case errors.Is(err, errSelfUpdateDisabled):
		writeErr(w, http.StatusForbidden, "self-update is disabled")
		return
	case errors.Is(err, errUpdateInProgress):
		writeErr(w, http.StatusConflict, "an update is already in progress")
		return
	case errors.Is(err, selfupdate.ErrUpToDate):
		writeErr(w, http.StatusConflict, "already up to date")
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "update failed: "+err.Error())
		return
	}
	s.audit(r, "update.apply", res.From, res.To)
	writeJSON(w, http.StatusOK, map[string]any{"from": res.From, "to": res.To, "restartRequired": true})
}

// handleRestart gracefully restarts the process (re-exec of the on-disk binary),
// applying a freshly-installed update. Admin-gated by the router.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if !s.update.selfUpdate {
		writeErr(w, http.StatusForbidden, "self-update is disabled")
		return
	}
	if s.onRestart == nil {
		writeErr(w, http.StatusNotImplemented, "in-app restart is not supported in this run mode")
		return
	}
	s.audit(r, "update.restart", "", "")
	writeJSON(w, http.StatusOK, map[string]bool{"restarting": true})
	// Fire after the handler returns so the response is flushed before the
	// server stops accepting connections.
	go s.onRestart()
}
