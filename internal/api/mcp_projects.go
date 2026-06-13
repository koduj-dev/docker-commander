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
	deployed := map[string]bool{}
	if stacks, err := s.docker.ListStacks(ctx, 0); err == nil {
		for _, st := range stacks {
			deployed[st.Project] = true
		}
	}
	out := make([]mcp.ManagedProject, 0, len(projs))
	for _, p := range projs {
		out = append(out, mcp.ManagedProject{ID: p.ID, Name: p.Name, Slug: p.Slug, Deployed: deployed[p.Slug]})
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
	return docker.ComposeUp(ctx, s.projectRoot(p.ID), p.Slug, profiles)
}

// mcpDownProject runs `docker compose down` for a managed project.
func (s *Server) mcpDownProject(ctx context.Context, id int64) (string, error) {
	p, err := s.store.ProjectByID(ctx, id)
	if err != nil {
		return "", err
	}
	return docker.ComposeDown(ctx, s.projectRoot(p.ID), p.Slug)
}
