package api

import (
	"context"
	"errors"

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
		out = append(out, mcp.ManagedProject{ID: p.ID, Name: p.Name, Slug: p.Slug, Deployed: deployed[p.HostID][p.Slug]})
	}
	return out, nil
}

// mcpDeployProject runs `docker compose up -d` for a managed project.
func (s *Server) mcpDeployProject(ctx context.Context, id int64, profiles []string) (string, error) {
	if !docker.ComposeAvailable(ctx) {
		return "", errors.New("the `docker compose` CLI is not available on the host running Docker Commander")
	}
	p, err := s.store.ProjectByID(ctx, id)
	if err != nil {
		return "", err
	}
	if p.HostID != 0 {
		// MCP tokens carry no per-host authorization, so remote-host deploys go
		// through the web UI (which enforces the "hosts" permission).
		return "", errors.New("this project targets a remote host; deploy it from the web UI")
	}
	dir := s.projectRoot(p.ID)
	env, cleanup, err := s.projectComposeEnv(ctx, p, dir)
	if err != nil {
		return "", err
	}
	defer cleanup()
	// Rebuild, matching the web UI. Not a widening of the MCP surface: `up`
	// already builds a service whose image is missing, so deploying a project
	// with a `build:` section could always run its Dockerfile. What this fixes is
	// the inconsistency where the same project deployed through MCP would keep
	// running a stale image while the UI refreshed it.
	return docker.ComposeUp(ctx, dir, p.Slug, profiles, env, true)
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
	if p.HostID != 0 {
		return "", errors.New("this project targets a remote host; manage it from the web UI")
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
	if p.HostID != 0 {
		// Same restriction as deploy: MCP tokens carry no per-host authorization,
		// and previewing a remote project means listing that host's containers.
		// Lifting this is a single change for both, and belongs with it.
		return out, errors.New("this project targets a remote host; preview it from the web UI")
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

	var running []docker.ServiceSpec
	if stacks, serr := s.docker.ListStacks(ctx, p.HostID); serr == nil {
		for i := range stacks {
			if stacks[i].Project == p.Slug {
				running = docker.RunningServices(&stacks[i])
				break
			}
		}
	}

	prev := docker.BuildDeployPreview(resolved, running)
	out.Valid = true
	out.Project = p.Name
	out.Services = prev.Services
	out.Running = prev.Running
	out.Changes = prev.Changes
	out.Unchanged = prev.Unchanged
	return out, nil
}
