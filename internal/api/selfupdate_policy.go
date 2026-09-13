package api

import (
	"context"
	"log"
	"time"

	"github.com/koduj-dev/docker-commander/internal/selfupdate"
	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/version"
)

// StartSelfUpdatePolicyLoop periodically applies a newer release automatically
// when an admin has opted into it, following the same cache TTL as the manual
// update check (there's no point polling for a policy decision more often than
// the underlying GitHub check itself refreshes). Runs until ctx is cancelled.
func (s *Server) StartSelfUpdatePolicyLoop(ctx context.Context) {
	t := time.NewTicker(updateCacheTTL)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tryAutoApply(ctx)
		}
	}
}

// tryAutoApply is the production entry point: delegate to autoApply bound to
// the real self-update apply function (which itself serialises against a
// concurrent manual apply via applyMu).
func (s *Server) tryAutoApply(ctx context.Context) {
	s.autoApply(ctx, s.update.apply)
}

// autoApply applies the latest release once shouldAutoApply's gates all pass,
// calling applyFn (production: the checker's own locked apply; tests: a fake)
// so the gating logic is verifiable without ever reaching the network. Any
// gate failing, or applyFn itself failing (including "already in progress"),
// is a silent no-op — the next tick tries again.
func (s *Server) autoApply(ctx context.Context, applyFn func(context.Context) (selfupdate.Result, error)) {
	pol, err := s.store.SelfUpdatePolicy(ctx)
	if err != nil {
		return
	}
	st := s.update.status(ctx)
	if !s.shouldAutoApply(pol, st) {
		return
	}
	res, err := applyFn(ctx)
	if err != nil {
		log.Printf("self-update: auto-apply failed: %v", err)
		return
	}
	_ = s.store.SetLastAutoUpdate(ctx, store.LastAutoUpdate{Version: res.To, AppliedAt: time.Now()})
	_ = s.store.Audit(ctx, store.AuditEntry{
		Username: "system", Action: "update.apply", Target: res.From,
		Detail: res.To + " (automatic, policy: " + pol.Granularity + ")",
	})
	go s.onRestart()
}

// shouldAutoApply is the pure decision behind autoApply: self-update must be
// enabled, the process must be able to restart itself, an admin must have
// opted into the policy, an update must actually be available, and its
// version delta must fall within the configured granularity ceiling.
func (s *Server) shouldAutoApply(pol store.SelfUpdatePolicy, st updateStatus) bool {
	if !s.update.enabled || !s.update.selfUpdate || s.onRestart == nil {
		return false
	}
	if !pol.Enabled || !st.UpdateAvailable {
		return false
	}
	return granularityAllows(pol.Granularity, version.Delta(st.Current, st.Latest))
}

// granularityAllows reports whether delta ("major"/"minor"/"patch") is within
// the chosen policy ceiling: "patch" allows only patch, "minor" allows
// minor+patch, "major" allows everything.
func granularityAllows(policy, delta string) bool {
	switch policy {
	case "major":
		return delta == "major" || delta == "minor" || delta == "patch"
	case "minor":
		return delta == "minor" || delta == "patch"
	case "patch":
		return delta == "patch"
	default:
		return false
	}
}
