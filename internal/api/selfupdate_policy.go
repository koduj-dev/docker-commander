package api

import (
	"context"
	"errors"
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
// the real checker's applyIfPolicyAllows.
func (s *Server) tryAutoApply(ctx context.Context) {
	s.autoApply(ctx, s.update.applyIfPolicyAllows)
}

// autoApply applies the latest release once autoApplyEnabled passes, calling
// applyFn (production: the checker's own locked, policy-checked apply; tests:
// a fake) so the gating logic is verifiable without ever reaching the
// network. The granularity ceiling is evaluated by applyFn against the EXACT
// release it resolves and is about to install — never against an earlier
// cached status — closing the TOCTOU gap a stale check would otherwise leave:
// a release published between two ticks, or even between an admin's last
// banner load and this call, can never slip past the gate. Any gate failing,
// or applyFn itself declining (already in progress, up to date, or the
// resolved release fails the policy check), is a silent no-op — the next
// tick tries again.
func (s *Server) autoApply(ctx context.Context, applyFn func(context.Context, func(string) bool) (selfupdate.Result, error)) {
	pol, err := s.store.SelfUpdatePolicy(ctx)
	if err != nil || !s.autoApplyEnabled(pol) {
		return
	}
	current := s.update.current
	res, err := applyFn(ctx, func(latestTag string) bool {
		return granularityAllows(pol.Granularity, version.Delta(current, latestTag))
	})
	if err != nil {
		// ErrUpToDate, ErrPolicyRefused, "already in progress" and "disabled" are
		// all expected "not now" outcomes on any given tick, not failures.
		if !errors.Is(err, selfupdate.ErrUpToDate) && !errors.Is(err, selfupdate.ErrPolicyRefused) &&
			!errors.Is(err, errUpdateInProgress) && !errors.Is(err, errSelfUpdateDisabled) {
			log.Printf("self-update: auto-apply failed: %v", err)
		}
		return
	}
	_ = s.store.SetLastAutoUpdate(ctx, store.LastAutoUpdate{Version: res.To, AppliedAt: time.Now()})
	_ = s.store.Audit(ctx, store.AuditEntry{
		Username: "system", Action: "update.apply", Target: res.From,
		Detail: res.To + " (automatic, policy: " + pol.Granularity + ")",
	})
	go s.onRestart()
}

// autoApplyEnabled is the pure, network-free half of the decision: self-update
// must be enabled, the process must be able to restart itself, and an admin
// must have opted into the policy. (Whether an update actually exists, and
// whether its granularity is within the chosen ceiling, is checked later by
// applyFn against the release it actually resolves — see autoApply's doc.)
func (s *Server) autoApplyEnabled(pol store.SelfUpdatePolicy) bool {
	return s.update.enabled && s.update.selfUpdate && s.onRestart != nil && pol.Enabled
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
