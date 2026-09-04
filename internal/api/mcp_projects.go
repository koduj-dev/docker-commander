package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/mcp"
)

// This file adapts the application's managed-project operations into the plain
// closures the MCP package expects, so internal/mcp never has to know about the
// project directory layout or the compose CLI. RBAC is enforced separately by
// the MCP tool dispatcher (the "projects" section), so these are unguarded
// mechanics only.

// mcpListProjects lists managed projects and whether each is currently deployed.
func (s *Server) mcpListProjects(ctx context.Context) ([]mcp.ManagedProject, error) {
	projs, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	// Deployed-state is per host: a project deployed to a remote host won't show
	// up in the local daemon's stacks. Probe each distinct target host once.
	deployed := map[int64]map[string]bool{}
	for _, p := range projs {
		if _, done := deployed[p.HostID]; done {
			continue
		}
		m := map[string]bool{}
		if stacks, err := s.docker.ListStacks(ctx, p.HostID); err == nil {
			for _, st := range stacks {
				m[st.Project] = true
			}
		}
		deployed[p.HostID] = m
	}
	out := make([]mcp.ManagedProject, 0, len(projs))
	for _, p := range projs {
		out = append(out, mcp.ManagedProject{ID: p.ID, Name: p.Name, Slug: p.Slug, HostID: p.HostID, Deployed: deployed[p.HostID][p.Slug]})
	}
	return out, nil
}

// mcpDeployProject runs `docker compose up -d` for a managed project.
//
// It runs the exact same evaluateDeployPolicy gate the REST deploy handler
// does (see policy_handlers.go): a block-mode violation always refuses, and
// an un-confirmed warn-mode one refuses with a message telling the caller to
// retry with confirmPolicyWarnings — MCP has no UI to click through, so the
// caller (the model, on the user's behalf) is the thing that must confirm.
func (s *Server) mcpDeployProject(ctx context.Context, id int64, profiles []string, confirmPolicyWarnings bool) (string, error) {
	if !docker.ComposeAvailable(ctx) {
		return "", errors.New("the `docker compose` CLI is not available on the host running Docker Commander")
	}
	p, err := s.store.ProjectByID(ctx, id)
	if err != nil {
		return "", err
	}
	dir := s.projectRoot(p.ID)
	// projectDeployEnv, NOT projectComposeEnv — the same resolver the web UI's
	// deploy uses. For a local project the two are identical, but for a remote
	// host only this one ships the project's bind-mount sources to the target and
	// repoints them, and only this one refuses binds from OUTSIDE the project
	// folder unless the project is explicitly opted in. Deploying through MCP with
	// the weaker resolver would have quietly produced a different deployment than
	// the same button in the UI, and skipped that refusal.
	env, files, note, cleanup, err := s.projectDeployEnv(ctx, p, dir)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// The MCP tool dispatcher (internal/mcp) audits the outcome of every
	// deploy attempt under "mcp.project.deploy", including this one when it
	// returns an error below — so a refused deploy is not silent, even though
	// it isn't broken out into its own policy_block/policy_warn_ack action
	// the way the REST audit trail is.
	profiles = docker.NormalizeProfiles(profiles)
	blocked, warned, perr := s.evaluateDeployPolicy(ctx, p.Slug, dir, profiles, env, files)
	if perr != nil {
		return "", fmt.Errorf("policy check failed, refusing to deploy for safety: %w", perr)
	}
	if len(blocked) > 0 {
		return "", fmt.Errorf("refused by policy (block): %s", policyViolationSummary(blocked))
	}
	if len(warned) > 0 && !confirmPolicyWarnings {
		return "", fmt.Errorf("policy warnings require confirmation (retry with confirm_policy_warnings=true): %s", policyViolationSummary(warned))
	}

	// Rebuild, matching the web UI. Not a widening of the MCP surface: `up`
	// already builds a service whose image is missing, so deploying a project
	// with a `build:` section could always run its Dockerfile. What this fixes is
	// the inconsistency where the same project deployed through MCP would keep
	// running a stale image while the UI refreshed it.
	out, err := docker.ComposeUpFiles(ctx, dir, p.Slug, profiles, env, files, true)
	if note != "" {
		// The note says which bind mounts were shipped or passed through to the
		// remote host. The UI shows it; without it here the model would report a
		// clean deploy and never mention that paths were remapped.
		out = strings.TrimRight(out, "\n") + "\nnote: " + note
	}
	return out, err
}

// mcpDownProject runs `docker compose down` for a managed project.
func (s *Server) mcpDownProject(ctx context.Context, id int64) (string, error) {
	if !docker.ComposeAvailable(ctx) {
		return "", errors.New("the `docker compose` CLI is not available on the host running Docker Commander")
	}
	p, err := s.store.ProjectByID(ctx, id)
	if err != nil {
		return "", err
	}
	dir := s.projectRoot(p.ID)
	env, cleanup, err := s.projectComposeEnv(ctx, p, dir)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return docker.ComposeDown(ctx, dir, p.Slug, env)
}

// mcpPreviewProject reports what deploying a managed project would change,
// without deploying it.
//
// Compares the resolved compose against the containers actually running under
// the project's label — not against a record of the last deploy. A stored record
// says what someone last asked for; the containers say what is there, and the two
// diverge exactly when it matters (a manual removal, a deploy that half-failed).
func (s *Server) mcpPreviewProject(ctx context.Context, id int64) (mcp.ProjectPreview, error) {
	var out mcp.ProjectPreview
	if !docker.ComposeAvailable(ctx) {
		return out, errors.New("the `docker compose` CLI is not available on the host running Docker Commander")
	}
	p, err := s.store.ProjectByID(ctx, id)
	if err != nil {
		return out, err
	}
	dir := s.projectRoot(p.ID)
	cfgJSON, err := docker.ComposeConfigJSON(ctx, dir, p.Slug)
	if err != nil {
		// An invalid compose file is the single most useful thing a preview can
		// report, so it comes back as a result rather than an error.
		out.Valid = false
		out.Error = err.Error()
		return out, nil
	}
	resolved, err := docker.ParseComposeServices(cfgJSON)
	if err != nil {
		out.Valid = false
		out.Error = err.Error()
		return out, nil
	}

	var containers []docker.StackContainer
	if stacks, serr := s.docker.ListStacks(ctx, p.HostID); serr == nil {
		for i := range stacks {
			if stacks[i].Project == p.Slug {
				containers = stacks[i].Containers
				break
			}
		}
	}
	// LiveServices pays for one inspect per service (vs. the free
	// RunningServices summary) so the preview can compare more than image —
	// env, ports, volumes, networks, restart policy, resource limits and
	// healthcheck. Worth it here: this is an explicit, user-triggered
	// preview, not a hot loop.
	running := s.docker.LiveServices(ctx, p.HostID, containers)

	prev := docker.BuildDeployPreview(resolved, running)
	// Best-effort: a mutable tag can point at a new image without the tag
	// string changing, which the plain comparison above can't see.
	s.docker.AugmentDigestDrift(ctx, p.HostID, &prev, containers)
	docker.ExtendServiceComparison(&prev, resolved, running)
	if ignores, ierr := s.store.ListDriftIgnores(ctx, p.ID); ierr == nil && len(ignores) > 0 {
		ignored := make(map[[3]string]bool, len(ignores))
		for _, ig := range ignores {
			ignored[[3]string{ig.Service, ig.Kind, ig.Fingerprint}] = true
		}
		docker.MarkIgnoredChanges(prev.Changes, ignored)
	}
	out.Valid = true
	out.Project = p.Name
	out.Services = prev.Services
	out.Running = prev.Running
	out.Changes = prev.Changes
	out.Unchanged = prev.Unchanged
	out.Active = docker.ActiveChanges(prev.Changes)
	return out, nil
}
