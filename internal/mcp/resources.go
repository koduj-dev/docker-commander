package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerResources exposes a small set of MCP resources — readable data a user
// can attach as context rather than invoking a tool. They share the same RBAC
// gate as tools (ReadResourceRequest carries a RequestExtra with the principal).
func (h *handler) registerResources(s *mcpsdk.Server) {
	s.AddResource(&mcpsdk.Resource{
		Name:        "container-inventory",
		URI:         "dc://inventory/containers",
		Description: "Current containers on the default host (JSON): id, name, image, state, status, ports.",
		MIMEType:    "application/json",
	}, h.resInventory)

	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name:        "compose-file",
		URITemplate: "dc://compose/{project}",
		Description: "The docker-compose.yml of a running Compose project (stack) on the default host. {project} is the project name from list_projects.",
		MIMEType:    "application/yaml",
	}, h.resCompose)
}

func (h *handler) resInventory(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	if _, err := h.authorizeExtra(ctx, req.Extra, "containers", false); err != nil {
		return nil, err
	}
	cs, err := h.deps.Docker.ListContainers(ctx, 0)
	if err != nil {
		return nil, err
	}
	briefs := []containerBrief{}
	for _, c := range cs {
		briefs = append(briefs, containerBrief{
			ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, Status: c.Status, Ports: portStrings(c.Ports),
		})
	}
	body, err := json.MarshalIndent(briefs, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: req.Params.URI, MIMEType: "application/json", Text: string(body),
	}}}, nil
}

func (h *handler) resCompose(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	if _, err := h.authorizeExtra(ctx, req.Extra, "projects", false); err != nil {
		return nil, err
	}
	project := strings.TrimPrefix(req.Params.URI, "dc://compose/")
	if project == "" || project == req.Params.URI {
		return nil, errors.New("malformed compose resource URI")
	}
	_, content, err := h.deps.Docker.StackComposeFile(ctx, 0, project)
	if err != nil {
		return nil, err
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: req.Params.URI, MIMEType: "application/yaml", Text: content,
	}}}, nil
}
