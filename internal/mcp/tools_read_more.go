package mcp

import (
	"context"
	"errors"
	"slices"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/history"
)

// ---- list_volumes ----

type volumeBrief struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Mountpoint string   `json:"mountpoint"`
	Scope      string   `json:"scope,omitempty"`
	InUseBy    []string `json:"inUseBy"`
}

type listVolumesOut struct {
	Volumes []volumeBrief `json:"volumes"`
}

func (h *handler) listVolumes(ctx context.Context, req *mcpsdk.CallToolRequest, in hostInput) (*mcpsdk.CallToolResult, listVolumesOut, error) {
	if _, err := h.authorize(ctx, req, "volumes", false); err != nil {
		return nil, listVolumesOut{}, err
	}
	vols, err := h.deps.Docker.ListVolumes(ctx, in.HostID)
	if err != nil {
		return nil, listVolumesOut{}, err
	}
	out := listVolumesOut{Volumes: []volumeBrief{}}
	for _, v := range vols {
		used := v.InUseBy
		if used == nil {
			used = []string{}
		}
		out.Volumes = append(out.Volumes, volumeBrief{
			Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope, InUseBy: used,
		})
	}
	return nil, out, nil
}

// ---- list_networks ----

type networkBrief struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Scope      string   `json:"scope,omitempty"`
	Internal   bool     `json:"internal"`
	Subnets    []string `json:"subnets"`
	Containers []string `json:"containers"`
}

type listNetworksOut struct {
	Networks []networkBrief `json:"networks"`
}

func (h *handler) listNetworks(ctx context.Context, req *mcpsdk.CallToolRequest, in hostInput) (*mcpsdk.CallToolResult, listNetworksOut, error) {
	if _, err := h.authorize(ctx, req, "networks", false); err != nil {
		return nil, listNetworksOut{}, err
	}
	nets, err := h.deps.Docker.ListNetworks(ctx, in.HostID)
	if err != nil {
		return nil, listNetworksOut{}, err
	}
	out := listNetworksOut{Networks: []networkBrief{}}
	for _, n := range nets {
		subnets, conts := n.Subnets, n.Containers
		if subnets == nil {
			subnets = []string{}
		}
		if conts == nil {
			conts = []string{}
		}
		out.Networks = append(out.Networks, networkBrief{
			ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope,
			Internal: n.Internal, Subnets: subnets, Containers: conts,
		})
	}
	return nil, out, nil
}

// ---- system_info ----

type systemInfoOut struct {
	HostName          string `json:"hostName"`
	ServerVersion     string `json:"serverVersion"`
	OperatingSystem   string `json:"operatingSystem"`
	OSType            string `json:"osType"`
	KernelVersion     string `json:"kernelVersion"`
	Architecture      string `json:"architecture"`
	CPUs              int    `json:"cpus"`
	MemTotal          int64  `json:"memTotal"`
	StorageDriver     string `json:"storageDriver"`
	LoggingDriver     string `json:"loggingDriver"`
	CgroupVersion     string `json:"cgroupVersion"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"containersRunning"`
	ContainersStopped int    `json:"containersStopped"`
	Images            int    `json:"images"`
}

func (h *handler) systemInfo(ctx context.Context, req *mcpsdk.CallToolRequest, in hostInput) (*mcpsdk.CallToolResult, systemInfoOut, error) {
	if _, err := h.authorize(ctx, req, "dashboard", false); err != nil {
		return nil, systemInfoOut{}, err
	}
	si, err := h.deps.Docker.SystemInfo(ctx, in.HostID)
	if err != nil {
		return nil, systemInfoOut{}, err
	}
	out := systemInfoOut{
		HostName: si.HostName, ServerVersion: si.ServerVersion, OperatingSystem: si.OperatingSystem,
		OSType: si.OSType, KernelVersion: si.KernelVersion, Architecture: si.Architecture,
		CPUs: si.CPUs, MemTotal: si.MemTotal, StorageDriver: si.StorageDriver,
		LoggingDriver: si.LoggingDriver, CgroupVersion: si.CgroupVersion,
		Containers: si.Containers, ContainersRunning: si.ContainersRunning,
		ContainersStopped: si.ContainersStopped, Images: si.Images,
	}
	return nil, out, nil
}

// ---- metrics_history ----

var rangeWindows = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
}

type metricsHistoryInput struct {
	ContainerID string `json:"container_id" jsonschema:"container ID"`
	Range       string `json:"range,omitempty" jsonschema:"time window: 15m, 1h, or 6h (default 1h)"`
}

type metricsPoint struct {
	T   int64    `json:"t"` // unix millis
	CPU *float64 `json:"cpu,omitempty"`
	Mem *float64 `json:"mem,omitempty"`
}

type metricsHistoryOut struct {
	Range  string         `json:"range"`
	Points []metricsPoint `json:"points"`
}

func (h *handler) metricsHistory(ctx context.Context, req *mcpsdk.CallToolRequest, in metricsHistoryInput) (*mcpsdk.CallToolResult, metricsHistoryOut, error) {
	if _, err := h.authorize(ctx, req, "dashboard", false); err != nil {
		return nil, metricsHistoryOut{}, err
	}
	if h.deps.History == nil {
		return nil, metricsHistoryOut{}, errors.New("metrics history is not available")
	}
	window, ok := rangeWindows[in.Range]
	if !ok {
		in.Range, window = "1h", time.Hour
	}
	since := time.Now().Add(-window)

	cpu, err := h.deps.History.Query(ctx, in.ContainerID, history.MetricCPU, since)
	if err != nil {
		return nil, metricsHistoryOut{}, err
	}
	mem, err := h.deps.History.Query(ctx, in.ContainerID, history.MetricMem, since)
	if err != nil {
		return nil, metricsHistoryOut{}, err
	}

	// Merge the two series by timestamp, mirroring the dashboard chart.
	byT := map[int64]*metricsPoint{}
	order := []int64{}
	get := func(t int64) *metricsPoint {
		if p, ok := byT[t]; ok {
			return p
		}
		p := &metricsPoint{T: t}
		byT[t] = p
		order = append(order, t)
		return p
	}
	for _, pt := range cpu {
		v := pt.V
		get(pt.T).CPU = &v
	}
	for _, pt := range mem {
		v := pt.V
		get(pt.T).Mem = &v
	}
	slices.Sort(order) // time-ascending
	out := metricsHistoryOut{Range: in.Range, Points: []metricsPoint{}}
	for _, t := range order {
		out.Points = append(out.Points, *byT[t])
	}
	return nil, out, nil
}

// ---- recent_audit ----

const auditDefaultLimit = 50
const auditMaxLimit = 200

type recentAuditInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many recent entries to return (default 50, max 200)"`
}

// auditEntryBrief deliberately omits the audit "detail" field. Detail is a
// free-form record of REST request context and can carry historical payloads
// (env values, registry credentials); exposing it over MCP would defeat the
// secret-omission applied everywhere else. who/what/when/where is enough here.
type auditEntryBrief struct {
	Time     string `json:"time"`
	Username string `json:"username"`
	Action   string `json:"action"`
	Target   string `json:"target,omitempty"`
	IP       string `json:"ip,omitempty"`
}

type recentAuditOut struct {
	Entries []auditEntryBrief `json:"entries"`
}

func (h *handler) recentAudit(ctx context.Context, req *mcpsdk.CallToolRequest, in recentAuditInput) (*mcpsdk.CallToolResult, recentAuditOut, error) {
	if _, err := h.authorize(ctx, req, "audit", false); err != nil {
		return nil, recentAuditOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	if limit > auditMaxLimit {
		limit = auditMaxLimit
	}
	entries, err := h.deps.Store.RecentAudit(ctx, limit, 0)
	if err != nil {
		return nil, recentAuditOut{}, err
	}
	out := recentAuditOut{Entries: []auditEntryBrief{}}
	for _, e := range entries {
		out.Entries = append(out.Entries, auditEntryBrief{
			Time: e.CreatedAt.Format(time.RFC3339), Username: e.Username,
			Action: e.Action, Target: e.Target, IP: e.IP,
		})
	}
	return nil, out, nil
}

// ---- recent_events ----

const (
	eventsDefaultMinutes = 30
	eventsMaxMinutes     = 360
	eventsDefaultLimit   = 100
	eventsMaxLimit       = 500
)

type recentEventsInput struct {
	HostID  int64 `json:"host_id,omitempty" jsonschema:"Docker host ID from list_hosts; 0 or omitted = the default local host"`
	Minutes int   `json:"minutes,omitempty" jsonschema:"how far back to look, in minutes (default 30, max 360)"`
	Limit   int   `json:"limit,omitempty" jsonschema:"max events to return (default 100, max 500)"`
}

type eventBrief struct {
	Time   string `json:"time"`
	Type   string `json:"type"`   // container | image | network | volume | …
	Action string `json:"action"` // start | die | pull | create | …
	Name   string `json:"name,omitempty"`
	ID     string `json:"id,omitempty"`
}

type recentEventsOut struct {
	Events []eventBrief `json:"events"`
}

func (h *handler) recentEvents(ctx context.Context, req *mcpsdk.CallToolRequest, in recentEventsInput) (*mcpsdk.CallToolResult, recentEventsOut, error) {
	if _, err := h.authorize(ctx, req, "events", false); err != nil {
		return nil, recentEventsOut{}, err
	}
	minutes := in.Minutes
	if minutes <= 0 {
		minutes = eventsDefaultMinutes
	}
	if minutes > eventsMaxMinutes {
		minutes = eventsMaxMinutes
	}
	limit := in.Limit
	if limit <= 0 {
		limit = eventsDefaultLimit
	}
	if limit > eventsMaxLimit {
		limit = eventsMaxLimit
	}
	evs, err := h.deps.Docker.RecentEvents(ctx, in.HostID, time.Duration(minutes)*time.Minute, limit)
	if err != nil {
		return nil, recentEventsOut{}, err
	}
	// Attr is deliberately omitted — event actor attributes can carry arbitrary
	// container labels (and thus secrets). Type/action/name/id are enough.
	out := recentEventsOut{Events: []eventBrief{}}
	for _, e := range evs {
		out.Events = append(out.Events, eventBrief{
			Time:   time.Unix(e.Time, 0).UTC().Format(time.RFC3339),
			Type:   e.Type,
			Action: e.Action,
			Name:   e.Name,
			ID:     shortID(e.ID),
		})
	}
	return nil, out, nil
}

// shortID truncates a long Docker ID to its 12-char prefix for compact output.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
