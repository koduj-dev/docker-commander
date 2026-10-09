package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// imageUpdatePollInterval matches the self-update checker's own cache TTL —
// there's no particular reason to check a registry for image drift on a
// different cadence than DC already checks GitHub for its own updates.
const imageUpdatePollInterval = 6 * time.Hour

// errProjectNotDeployed means the project has no running containers to check
// — a normal, frequent case (never deployed, or currently down), not a
// failure worth logging.
var errProjectNotDeployed = errors.New("project is not currently deployed")

// StartImageUpdatePollLoop periodically checks every project's running
// services for a newer image at the registry (see NEXT.md's "Controlled
// image updates") and notifies once per newly-observed digest. This is
// detection + notification only — nothing here applies an update; see
// internal/docker's AugmentDigestDrift, which this reuses, for the same
// check already done on demand by the deploy preview. Runs until ctx is
// cancelled.
func (s *Server) StartImageUpdatePollLoop(ctx context.Context) {
	t := time.NewTicker(imageUpdatePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pollImageUpdates(ctx)
		}
	}
}

// pollImageUpdates checks every eligible project. Best-effort per project
// from here on: one project's failure (compose file broken, registry
// timeout) must never stop the rest from being checked.
func (s *Server) pollImageUpdates(ctx context.Context) {
	if !docker.ComposeAvailable(ctx) {
		return
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return
	}
	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		// An unusable host inventory means neither "is this host disabled"
		// nor "what's its name" can be answered correctly — proceeding
		// would poll projects on a disabled host and mislabel their host
		// name, so skip this entire tick rather than guess.
		return
	}
	for _, p := range projectsToPoll(projects, hosts) {
		p := p
		s.pollImageUpdatesForProject(ctx, &p, hostForProject(p.HostID, hosts), s.buildProjectImagePreviewChecked)
	}
}

// hostForProject resolves a project's target host. A project's own HostID
// convention uses 0 to mean "the local daemon," but store.ListHosts never
// returns a row with id 0 — the local host has a real row (from
// EnsureLocalHost) with a real, non-zero, auto-incremented id. So 0 is
// treated as an alias for whichever host has Kind=="local", the same
// resolution the rest of the store already relies on elsewhere.
func hostForProject(hostID int64, hosts []store.Host) *store.Host {
	for i := range hosts {
		if hosts[i].ID == hostID || (hostID == 0 && hosts[i].Kind == "local") {
			return &hosts[i]
		}
	}
	return nil
}

// projectsToPoll drops any project whose target host is explicitly
// disabled — mirroring monitoredHosts' own "skip disabled hosts" rule in
// internal/monitor, via hostForProject's 0-is-local alias so a disabled
// LOCAL host is caught too, not just a disabled remote one.
func projectsToPoll(projects []store.Project, hosts []store.Host) []store.Project {
	out := make([]store.Project, 0, len(projects))
	for _, p := range projects {
		if h := hostForProject(p.HostID, hosts); h != nil && h.Disabled {
			continue
		}
		out = append(out, p)
	}
	return out
}

// pollImageUpdatesForProject runs one project's digest-drift check (via
// buildPreview, injected so this orchestration is testable without a live
// compose CLI/registry) and notifies about anything newly drifted. host may
// be nil (host inventory couldn't resolve it, e.g. it was deleted between
// listing and here) — the event still gets a sane "local"/id-0 fallback.
func (s *Server) pollImageUpdatesForProject(ctx context.Context, p *store.Project, host *store.Host, buildPreview func(context.Context, *store.Project) (docker.DeployPreview, map[string]bool, error)) {
	prev, confirmedUnchanged, err := buildPreview(ctx, p)
	if err != nil {
		return
	}

	lastNotified, err := s.store.LastNotifiedImageDigests(ctx, p.ID)
	if err != nil {
		lastNotified = map[string]string{}
	}

	digestChanged := make(map[string]bool, len(prev.Changes))
	for _, ch := range prev.Changes {
		if ch.Kind == "digest" {
			digestChanged[ch.Service] = true
		}
	}
	// A currently-running service positively confirmed unchanged (both the
	// registry and the running-container digest resolved, and matched) has
	// resolved — redeployed, or the registry reverted to what's running —
	// so clear any stale dedup state for it: otherwise a LATER drift back to
	// that same old digest would be silently suppressed by state that
	// describes a situation which no longer exists. A service that is
	// merely ABSENT from digestChanged but NOT in confirmedUnchanged (a
	// registry timeout, auth failure, or inspect error) is left alone —
	// clearing it there would let a still-unresolved, already-notified
	// drift renotify on every registry hiccup.
	for _, svc := range prev.Running {
		if digestChanged[svc.Name] || !confirmedUnchanged[svc.Name] {
			continue
		}
		if _, ok := lastNotified[svc.Name]; ok {
			_ = s.store.SetLastNotifiedImageDigest(ctx, p.ID, svc.Name, "")
		}
	}

	hostName, alertHostID := "local", p.HostID
	if host != nil {
		hostName, alertHostID = host.Name, host.ID
	}
	for _, ch := range imageUpdatesToNotify(prev.Changes, lastNotified) {
		ev := &store.AlertEvent{
			RuleName: "Image update", Type: "image_update", Severity: "info",
			HostID: alertHostID, HostName: hostName,
			Project: p.Slug, ContainerName: ch.Service,
			Message: fmt.Sprintf("A newer image is available for service %q in project %q: %s", ch.Service, p.Name, ch.Detail),
		}
		if err := s.monitor.NotifySystem(ev); err != nil {
			continue // never recorded — don't mark this digest as notified
		}
		_ = s.store.SetLastNotifiedImageDigest(ctx, p.ID, ch.Service, ch.To)
	}
}

// buildProjectImagePreviewChecked resolves one project's deploy preview —
// compose vs. running services, with registry digest drift merged in — the
// same computation the on-demand deploy preview performs (see
// internal/api/mcp_projects.go's mcpPreviewProject), just callable on a
// schedule instead of only when a human opens it, and additionally
// returning which services AugmentDigestDriftChecked positively confirmed
// unchanged (see its doc comment — the poller's dedup-state clearing needs
// this to avoid treating a failed registry/inspect lookup the same as a
// confirmed resolve). Resolved with every declared Compose profile enabled
// (not just the default set), so a service gated behind `profiles:` is still
// checked — the same fix domain-mapping validation needed for the same
// underlying reason (a plain `compose config` silently omits profile-gated
// services) — and against the project's own recorded compose filename for
// BOTH the profile list and the config itself, not compose's default-name
// auto-discovery, which a non-default filename would either fail outright or
// silently resolve profiles from the wrong file.
func (s *Server) buildProjectImagePreviewChecked(ctx context.Context, p *store.Project) (docker.DeployPreview, map[string]bool, error) {
	stacks, err := s.docker.ListStacks(ctx, p.HostID)
	if err != nil {
		return docker.DeployPreview{}, nil, err
	}
	var stack *docker.Stack
	for i := range stacks {
		if stacks[i].Project == p.Slug {
			stack = &stacks[i]
			break
		}
	}
	if stack == nil {
		return docker.DeployPreview{}, nil, errProjectNotDeployed
	}
	running := make([]docker.StackContainer, 0, len(stack.Containers))
	for _, c := range stack.Containers {
		if c.State == "running" {
			running = append(running, c)
		}
	}
	if len(running) == 0 {
		return docker.DeployPreview{}, nil, errProjectNotDeployed
	}

	dir := s.projectRoot(p.ID)
	_, masked, _, serr := s.projectSecretEnvs(ctx, p.ID)
	if serr != nil {
		return docker.DeployPreview{}, nil, serr
	}
	files := []string{p.ComposeFile}
	profiles, perr := docker.ComposeProfilesEnvFiles(ctx, dir, p.Slug, masked, files)
	if perr != nil {
		profiles = nil // best-effort: fall back to the default-profile set rather than failing the whole project
	}
	cfgJSON, err := docker.ComposeConfigJSONFiles(ctx, dir, p.Slug, profiles, masked, files)
	if err != nil {
		return docker.DeployPreview{}, nil, err
	}
	resolved, err := docker.ParseComposeServices(cfgJSON)
	if err != nil {
		return docker.DeployPreview{}, nil, err
	}

	runningServices := docker.RunningServices(&docker.Stack{Project: stack.Project, Containers: running})
	prev := docker.BuildDeployPreview(resolved, runningServices)
	confirmedUnchanged := s.docker.AugmentDigestDriftChecked(ctx, p.HostID, &prev, running)
	return prev, confirmedUnchanged, nil
}

// imageUpdatesToNotify filters a deploy preview's changes down to genuinely
// NEW digest drift — a "digest" change whose target differs from what was
// last notified for that service. This is what makes polling idempotent: a
// still-unresolved drift found again on the next tick is not renotified, but
// a digest that moves again is (and pollImageUpdatesForProject's own
// stale-state clearing above is what makes a REVERTED-then-repeated drift
// notify again too, since this function alone only ever compares against
// whatever is currently stored).
func imageUpdatesToNotify(changes []docker.ServiceChange, lastNotified map[string]string) []docker.ServiceChange {
	var out []docker.ServiceChange
	for _, ch := range changes {
		if ch.Kind != "digest" {
			continue
		}
		if lastNotified[ch.Service] == ch.To {
			continue
		}
		out = append(out, ch)
	}
	return out
}
