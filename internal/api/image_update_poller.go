package api

import (
	"context"
	"fmt"
	"time"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// imageUpdatePollInterval matches the self-update checker's own cache TTL —
// there's no particular reason to check a registry for image drift on a
// different cadence than DC already checks GitHub for its own updates.
const imageUpdatePollInterval = 6 * time.Hour

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

// pollImageUpdates checks every project. Best-effort throughout: one
// project's failure (compose file broken, host unreachable, registry
// timeout) must never stop the rest from being checked.
func (s *Server) pollImageUpdates(ctx context.Context) {
	if !docker.ComposeAvailable(ctx) {
		return
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return
	}
	hosts, _ := s.store.ListHosts(ctx)
	hostByID := make(map[int64]*store.Host, len(hosts))
	for i := range hosts {
		hostByID[hosts[i].ID] = &hosts[i]
	}
	for i := range projects {
		p := &projects[i]
		if h := hostByID[p.HostID]; h != nil && h.Disabled {
			continue
		}
		s.pollImageUpdatesForProject(ctx, p, hostByID[p.HostID])
	}
}

// pollImageUpdatesForProject checks one project's running services against
// their compose-declared image, resolving each running service's registry
// digest exactly like the deploy preview does — the difference is this runs
// on a schedule for every project, not only when a human opens the preview.
func (s *Server) pollImageUpdatesForProject(ctx context.Context, p *store.Project, host *store.Host) {
	stacks, err := s.docker.ListStacks(ctx, p.HostID)
	if err != nil {
		return
	}
	var stack *docker.Stack
	for i := range stacks {
		if stacks[i].Project == p.Slug {
			stack = &stacks[i]
			break
		}
	}
	if stack == nil || len(stack.Containers) == 0 {
		return // not currently deployed — nothing running to check
	}

	dir := s.projectRoot(p.ID)
	_, masked, _, serr := s.projectSecretEnvs(ctx, p.ID)
	if serr != nil {
		return
	}
	cfgJSON, err := docker.ComposeConfigJSONFiles(ctx, dir, p.Slug, nil, masked, nil)
	if err != nil {
		return
	}
	resolved, err := docker.ParseComposeServices(cfgJSON)
	if err != nil {
		return
	}

	running := docker.RunningServices(stack)
	prev := docker.BuildDeployPreview(resolved, running)
	s.docker.AugmentDigestDrift(ctx, p.HostID, &prev, stack.Containers)

	lastNotified, err := s.store.LastNotifiedImageDigests(ctx, p.ID)
	if err != nil {
		lastNotified = map[string]string{}
	}

	hostName := "local"
	if host != nil {
		hostName = host.Name
	}
	for _, ch := range imageUpdatesToNotify(prev.Changes, lastNotified) {
		s.monitor.NotifySystem(&store.AlertEvent{
			Type: "image_update", Severity: "info",
			HostID: p.HostID, HostName: hostName,
			Project: p.Slug, ContainerName: ch.Service,
			Message: fmt.Sprintf("A newer image is available for service %q in project %q: %s", ch.Service, p.Name, ch.Detail),
		})
		_ = s.store.SetLastNotifiedImageDigest(ctx, p.ID, ch.Service, ch.To)
	}
}

// imageUpdatesToNotify filters a deploy preview's changes down to genuinely
// NEW digest drift — a "digest" change whose target differs from what was
// last notified for that service. This is what makes polling idempotent: a
// still-unresolved drift found again on the next tick is not renotified, but
// a digest that moves again (or reverts to what's running, clearing the
// entry implicitly since a future different value differs again) is.
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
