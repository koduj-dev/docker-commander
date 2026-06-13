package mcp

import (
	"context"
	"strconv"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// registerReadTools installs the read-only allowlist. Every tool is gated by a
// (section, write=false) authorize() call. None of these expose secrets: there
// is no arbitrary file read, no image export, no exec, no volume browsing, and
// container environment variables are deliberately omitted from get_container.
func (h *handler) registerReadTools(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_hosts",
		Description: "List the Docker hosts this server manages, with their IDs. Use a host_id from here in other tools (0 or omitted = the default local host).",
	}, h.listHosts)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_containers",
		Description: "List containers on a host (id, name, image, state, status, published ports).",
	}, h.listContainers)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "get_container",
		Description: "Inspect one container. Returns config and runtime metadata. Environment variables are intentionally NOT included.",
	}, h.getContainer)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "container_logs",
		Description: "Fetch the tail of a container's logs (bounded: at most a few hundred recent lines, truncated if large). May contain application output.",
	}, h.containerLogs)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_images",
		Description: "List images on a host (tags, size, age, whether in use).",
	}, h.listImages)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_projects",
		Description: "List Docker Compose projects (stacks) on a host with their running/total container counts.",
	}, h.listProjects)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "get_compose",
		Description: "Return the docker-compose.yml of a Compose project on a host (path + content). May contain configuration secrets.",
	}, h.getCompose)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "stats_overview",
		Description: "Snapshot of host CPU/memory and per-container resource usage.",
	}, h.statsOverview)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_volumes",
		Description: "List volumes on a host (name, driver, mountpoint, which containers use them). Contents are never exposed.",
	}, h.listVolumes)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_networks",
		Description: "List networks on a host (name, driver, scope, subnets, attached containers).",
	}, h.listNetworks)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "system_info",
		Description: "Docker engine and host info: versions, OS/kernel, CPU/memory, storage/logging drivers, container/image counts.",
	}, h.systemInfo)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "metrics_history",
		Description: "Historical CPU% and memory% for one container over a time range (15m, 1h, 6h).",
	}, h.metricsHistory)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "recent_audit",
		Description: "Recent entries from the application audit log (who did what, when). Behind the 'audit' section — most tokens will not have access.",
	}, h.recentAudit)
}

// ---- shared input ----

type hostInput struct {
	HostID int64 `json:"host_id,omitempty" jsonschema:"Docker host ID from list_hosts; 0 or omitted = the default local host"`
}

type containerInput struct {
	HostID      int64  `json:"host_id,omitempty" jsonschema:"Docker host ID from list_hosts; 0 or omitted = the default local host"`
	ContainerID string `json:"container_id" jsonschema:"container ID or name"`
}

// ---- list_hosts ----

type hostBrief struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Address  string `json:"address,omitempty"`
	Disabled bool   `json:"disabled"`
}

type listHostsOut struct {
	Hosts []hostBrief `json:"hosts"`
}

func (h *handler) listHosts(ctx context.Context, req *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, listHostsOut, error) {
	if _, err := h.authorize(ctx, req, "hosts", false); err != nil {
		return nil, listHostsOut{}, err
	}
	hosts, err := h.deps.Store.ListHosts(ctx)
	if err != nil {
		return nil, listHostsOut{}, err
	}
	out := listHostsOut{Hosts: []hostBrief{}}
	for _, ho := range hosts {
		out.Hosts = append(out.Hosts, hostBrief{
			ID: ho.ID, Name: ho.Name, Kind: ho.Kind, Address: ho.Address, Disabled: ho.Disabled,
		})
	}
	return nil, out, nil
}

// ---- list_containers ----

type containerBrief struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Image  string   `json:"image"`
	State  string   `json:"state"`
	Status string   `json:"status"`
	Ports  []string `json:"ports"`
}

type listContainersOut struct {
	Containers []containerBrief `json:"containers"`
}

func (h *handler) listContainers(ctx context.Context, req *mcpsdk.CallToolRequest, in hostInput) (*mcpsdk.CallToolResult, listContainersOut, error) {
	if _, err := h.authorize(ctx, req, "containers", false); err != nil {
		return nil, listContainersOut{}, err
	}
	cs, err := h.deps.Docker.ListContainers(ctx, in.HostID)
	if err != nil {
		return nil, listContainersOut{}, err
	}
	out := listContainersOut{Containers: []containerBrief{}}
	for _, c := range cs {
		out.Containers = append(out.Containers, containerBrief{
			ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, Status: c.Status,
			Ports: portStrings(c.Ports),
		})
	}
	return nil, out, nil
}

// ---- get_container ----

type containerDetailOut struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	State         string            `json:"state"`
	Status        string            `json:"status"`
	Health        string            `json:"health,omitempty"`
	Created       string            `json:"created"`
	StartedAt     string            `json:"startedAt,omitempty"`
	RestartCount  int               `json:"restartCount"`
	Command       []string          `json:"command"`
	Labels        map[string]string `json:"labels"`
	Ports         []string          `json:"ports"`
	Mounts        []string          `json:"mounts"`
	RestartPolicy string            `json:"restartPolicy,omitempty"`
}

func (h *handler) getContainer(ctx context.Context, req *mcpsdk.CallToolRequest, in containerInput) (*mcpsdk.CallToolResult, containerDetailOut, error) {
	if _, err := h.authorize(ctx, req, "containers", false); err != nil {
		return nil, containerDetailOut{}, err
	}
	d, err := h.deps.Docker.InspectContainer(ctx, in.HostID, in.ContainerID)
	if err != nil {
		return nil, containerDetailOut{}, err
	}
	// NOTE: d.Env is deliberately omitted — environment variables routinely
	// hold credentials. Do not add it here.
	mounts := []string{}
	for _, m := range d.Mounts {
		mounts = append(mounts, mountString(m))
	}
	cmd := d.Command
	if cmd == nil {
		cmd = []string{}
	}
	out := containerDetailOut{
		ID: d.ID, Name: d.Name, Image: d.Image, State: d.State, Status: d.Status,
		Health: d.Health, Created: d.Created, StartedAt: d.StartedAt,
		RestartCount: d.RestartCount, Command: cmd, Labels: d.Labels,
		Ports: portStrings(d.Ports), Mounts: mounts, RestartPolicy: d.RestartPolicy,
	}
	return nil, out, nil
}

// ---- container_logs ----

const (
	logsDefaultTail = 200
	logsMaxTail     = 1000
	logsMaxBytes    = 64 * 1024 // hard cap so logs can't be a bulk-exfil channel
)

type logsInput struct {
	HostID      int64  `json:"host_id,omitempty" jsonschema:"Docker host ID from list_hosts; 0 or omitted = the default local host"`
	ContainerID string `json:"container_id" jsonschema:"container ID or name"`
	Tail        int    `json:"tail,omitempty" jsonschema:"number of recent lines to return (default 200, max 1000)"`
}

type logLine struct {
	Stream  string `json:"stream"`
	Message string `json:"message"`
	Time    string `json:"time,omitempty"`
}

type logsOut struct {
	Lines     []logLine `json:"lines"`
	Truncated bool      `json:"truncated"`
}

func (h *handler) containerLogs(ctx context.Context, req *mcpsdk.CallToolRequest, in logsInput) (*mcpsdk.CallToolResult, logsOut, error) {
	if _, err := h.authorize(ctx, req, "logs", false); err != nil {
		return nil, logsOut{}, err
	}
	tail := in.Tail
	if tail <= 0 {
		tail = logsDefaultTail
	}
	if tail > logsMaxTail {
		tail = logsMaxTail
	}

	out := logsOut{Lines: []logLine{}}
	bytesSoFar := 0
	// StreamLogs invokes emit from two scanner goroutines (stdout + stderr)
	// concurrently, so the callback must serialize access to the shared output.
	var mu sync.Mutex
	err := h.deps.Docker.StreamLogs(ctx, in.HostID, in.ContainerID, false, strconv.Itoa(tail), func(l docker.LogLine) {
		mu.Lock()
		defer mu.Unlock()
		if out.Truncated {
			return
		}
		if bytesSoFar+len(l.Message) > logsMaxBytes {
			out.Truncated = true
			return
		}
		bytesSoFar += len(l.Message)
		out.Lines = append(out.Lines, logLine{Stream: l.Stream, Message: l.Message, Time: l.Timestamp})
	})
	if err != nil {
		return nil, logsOut{}, err
	}
	return nil, out, nil
}

// ---- list_images ----

type imageBrief struct {
	ID       string   `json:"id"`
	RepoTags []string `json:"repoTags"`
	Size     int64    `json:"size"`
	Created  int64    `json:"created"`
	InUse    bool     `json:"inUse"`
	Dangling bool     `json:"dangling"`
}

type listImagesOut struct {
	Images []imageBrief `json:"images"`
}

func (h *handler) listImages(ctx context.Context, req *mcpsdk.CallToolRequest, in hostInput) (*mcpsdk.CallToolResult, listImagesOut, error) {
	if _, err := h.authorize(ctx, req, "images", false); err != nil {
		return nil, listImagesOut{}, err
	}
	imgs, err := h.deps.Docker.ListImages(ctx, in.HostID)
	if err != nil {
		return nil, listImagesOut{}, err
	}
	out := listImagesOut{Images: []imageBrief{}}
	for _, im := range imgs {
		tags := im.RepoTags
		if tags == nil {
			tags = []string{}
		}
		out.Images = append(out.Images, imageBrief{
			ID: im.ID, RepoTags: tags, Size: im.Size, Created: im.Created,
			InUse: im.InUse, Dangling: im.Dangling,
		})
	}
	return nil, out, nil
}

// ---- list_projects ----

type projectBrief struct {
	Project    string `json:"project"`
	Running    int    `json:"running"`
	Containers int    `json:"containers"`
	ConfigFile string `json:"configFile,omitempty"`
}

type listProjectsOut struct {
	Projects []projectBrief `json:"projects"`
}

func (h *handler) listProjects(ctx context.Context, req *mcpsdk.CallToolRequest, in hostInput) (*mcpsdk.CallToolResult, listProjectsOut, error) {
	if _, err := h.authorize(ctx, req, "projects", false); err != nil {
		return nil, listProjectsOut{}, err
	}
	stacks, err := h.deps.Docker.ListStacks(ctx, in.HostID)
	if err != nil {
		return nil, listProjectsOut{}, err
	}
	out := listProjectsOut{Projects: []projectBrief{}}
	for _, st := range stacks {
		out.Projects = append(out.Projects, projectBrief{
			Project: st.Project, Running: st.Running, Containers: len(st.Containers), ConfigFile: st.ConfigFile,
		})
	}
	return nil, out, nil
}

// ---- get_compose ----

type composeInput struct {
	HostID  int64  `json:"host_id,omitempty" jsonschema:"Docker host ID from list_hosts; 0 or omitted = the default local host"`
	Project string `json:"project" jsonschema:"Compose project name from list_projects"`
}

type composeOut struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (h *handler) getCompose(ctx context.Context, req *mcpsdk.CallToolRequest, in composeInput) (*mcpsdk.CallToolResult, composeOut, error) {
	if _, err := h.authorize(ctx, req, "projects", false); err != nil {
		return nil, composeOut{}, err
	}
	path, content, err := h.deps.Docker.StackComposeFile(ctx, in.HostID, in.Project)
	if err != nil {
		return nil, composeOut{}, err
	}
	return nil, composeOut{Path: path, Content: content}, nil
}

// ---- stats_overview ----

type usageBrief struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpuPercent"`
	MemBytes   uint64  `json:"memBytes"`
	MemPercent float64 `json:"memPercent"`
}

type statsOut struct {
	CPUs       int          `json:"cpus"`
	MemTotal   int64        `json:"memTotal"`
	Containers []usageBrief `json:"containers"`
}

func (h *handler) statsOverview(ctx context.Context, req *mcpsdk.CallToolRequest, in hostInput) (*mcpsdk.CallToolResult, statsOut, error) {
	if _, err := h.authorize(ctx, req, "dashboard", false); err != nil {
		return nil, statsOut{}, err
	}
	ov, err := h.deps.Docker.ResourceOverview(ctx, in.HostID)
	if err != nil {
		return nil, statsOut{}, err
	}
	out := statsOut{CPUs: ov.CPUs, MemTotal: ov.MemTotal, Containers: []usageBrief{}}
	for _, u := range ov.Containers {
		out.Containers = append(out.Containers, usageBrief{
			ID: u.ID, Name: u.Name, CPUPercent: u.CPUPercent, MemBytes: u.MemBytes, MemPercent: u.MemPercent,
		})
	}
	return nil, out, nil
}

// ---- helpers ----

func portStrings(ports []docker.PortMapping) []string {
	out := []string{}
	for _, p := range ports {
		out = append(out, portString(p))
	}
	return out
}
