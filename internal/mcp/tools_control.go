package mcp

import (
	"context"
	"strconv"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerControlTools installs the "safe control" allowlist: lifecycle actions
// that are reversible and non-destructive. Every tool is write-gated (a
// read-only user OR a read-only token is rejected) and audited under the acting
// user. Deliberately ABSENT: remove, kill, prune, exec, and any image/volume
// deletion — those are not safe-by-default and are out of scope.
func (h *handler) registerControlTools(s *mcpsdk.Server) {
	for _, a := range []struct {
		name, action, verb string
	}{
		{"start_container", "start", "Start"},
		{"stop_container", "stop", "Stop"},
		{"restart_container", "restart", "Restart"},
	} {
		mcpsdk.AddTool(s, &mcpsdk.Tool{
			Name:        a.name,
			Description: a.verb + " a container by ID or name.",
		}, h.containerActionTool(a.action))
	}

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_managed_projects",
		Description: "List the application's managed Compose projects (id, name, slug, whether currently deployed). Use a project id with deploy_project / down_project.",
	}, h.listManagedProjects)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "deploy_project",
		Description: "Deploy a managed Compose project (docker compose up -d). Reversible via down_project.",
	}, h.deployProject)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "down_project",
		Description: "Bring a managed Compose project down (docker compose down — stops and removes its containers; named volumes are kept).",
	}, h.downProject)
}

// ---- list_managed_projects (read) ----

type managedProjectsOut struct {
	Projects []ManagedProject `json:"projects"`
}

func (h *handler) listManagedProjects(ctx context.Context, req *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, managedProjectsOut, error) {
	if _, err := h.authorize(ctx, req, "projects", false, 0); err != nil {
		return nil, managedProjectsOut{}, err
	}
	if h.deps.ListProjects == nil {
		return nil, managedProjectsOut{}, errProjectsUnavailable
	}
	projs, err := h.deps.ListProjects(ctx)
	if err != nil {
		return nil, managedProjectsOut{}, err
	}
	if projs == nil {
		projs = []ManagedProject{}
	}
	return nil, managedProjectsOut{Projects: projs}, nil
}

// ---- deploy_project / down_project (write) ----

type projectInput struct {
	ProjectID int64    `json:"project_id" jsonschema:"managed project id from list_managed_projects"`
	Profiles  []string `json:"profiles,omitempty" jsonschema:"optional compose profiles to activate (deploy only)"`
}

func (h *handler) deployProject(ctx context.Context, req *mcpsdk.CallToolRequest, in projectInput) (*mcpsdk.CallToolResult, actionResult, error) {
	p, err := h.authorize(ctx, req, "projects", true, 0)
	if err != nil {
		return nil, actionResult{}, err
	}
	if h.deps.DeployProject == nil {
		return nil, actionResult{}, errProjectsUnavailable
	}
	if in.ProjectID <= 0 {
		return nil, actionResult{}, errInvalidProject
	}
	if err := h.authorizeProjectHost(ctx, req, in.ProjectID, true); err != nil {
		return nil, actionResult{}, err
	}
	out, derr := h.deps.DeployProject(ctx, in.ProjectID, in.Profiles)
	res := actionResult{OK: derr == nil, Action: "deploy", Target: projectTarget(in.ProjectID), Output: out}
	h.audit(p, "mcp.project.deploy", res.Target, outcome(derr))
	if derr != nil {
		res.Output = combineErr(out, derr) // surface compose output to the model, not a bare error
	}
	return nil, res, nil
}

func (h *handler) downProject(ctx context.Context, req *mcpsdk.CallToolRequest, in projectInput) (*mcpsdk.CallToolResult, actionResult, error) {
	p, err := h.authorize(ctx, req, "projects", true, 0)
	if err != nil {
		return nil, actionResult{}, err
	}
	if h.deps.DownProject == nil {
		return nil, actionResult{}, errProjectsUnavailable
	}
	if in.ProjectID <= 0 {
		return nil, actionResult{}, errInvalidProject
	}
	if err := h.authorizeProjectHost(ctx, req, in.ProjectID, true); err != nil {
		return nil, actionResult{}, err
	}
	out, derr := h.deps.DownProject(ctx, in.ProjectID)
	res := actionResult{OK: derr == nil, Action: "down", Target: projectTarget(in.ProjectID), Output: out}
	h.audit(p, "mcp.project.down", res.Target, outcome(derr))
	if derr != nil {
		res.Output = combineErr(out, derr)
	}
	return nil, res, nil
}

// authorizeProjectHost re-checks the caller against the host a managed project
// actually targets. A project names its host in its own record, not in the tool
// arguments, so without this a role scoped to staging could deploy a project
// pointing at production — the section check alone would pass. An unreadable
// project is treated as a denial rather than as host 0.
func (h *handler) authorizeProjectHost(ctx context.Context, req *mcpsdk.CallToolRequest, projectID int64, write bool) error {
	proj, err := h.deps.Store.ProjectByID(ctx, projectID)
	if err != nil {
		return errInvalidProject
	}
	if proj.HostID == 0 {
		return nil
	}
	// Targeting a remote host needs the "hosts" section too, matching the REST
	// rule in api.requireHostAccess.
	if _, err := h.authorize(ctx, req, "hosts", false, proj.HostID); err != nil {
		return err
	}
	_, err = h.authorize(ctx, req, "projects", write, proj.HostID)
	return err
}

func projectTarget(id int64) string { return "project#" + strconv.FormatInt(id, 10) }

// outcome renders an audit detail string from an action's error (or success).
func outcome(err error) string {
	if err != nil {
		return "via MCP — failed: " + err.Error()
	}
	return "via MCP"
}

func combineErr(out string, err error) string {
	if out == "" {
		return "error: " + err.Error()
	}
	return out + "\nerror: " + err.Error()
}

// containerActionTool builds a write-gated handler for one container lifecycle
// action. The action string is fixed by us (never taken from model input), so
// the model can only ever trigger start/stop/restart.
func (h *handler) containerActionTool(action string) mcpsdk.ToolHandlerFor[containerInput, actionResult] {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in containerInput) (*mcpsdk.CallToolResult, actionResult, error) {
		p, err := h.authorize(ctx, req, "containers", true, in.HostID)
		if err != nil {
			return nil, actionResult{}, err
		}
		if in.ContainerID == "" {
			return nil, actionResult{}, errEmptyContainer
		}
		// Audit the attempt and its outcome (the security model leans on the
		// audit log, so failed/attempted actions are recorded too).
		err = h.deps.Docker.ContainerAction(ctx, in.HostID, in.ContainerID, action)
		h.audit(p, "mcp.container."+action, in.ContainerID, outcome(err))
		if err != nil {
			return nil, actionResult{}, err
		}
		return nil, actionResult{OK: true, Action: action, Target: in.ContainerID}, nil
	}
}

type actionResult struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Target string `json:"target"`
	Output string `json:"output,omitempty"`
}
