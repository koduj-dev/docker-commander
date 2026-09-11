package mcp

import (
	"context"
	"errors"
	"strconv"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Maintenance window tools: let an agent declare (and end) a planned-work
// silence — suppresses alert delivery for a scope and time without turning
// the engine's observation off. See internal/monitor's suppression check in
// emit()/fireHostAlert() for where this actually takes effect. Editing an
// existing window's scope/schedule is REST/UI-only by design: an ad-hoc
// "silence this for now, I'm working on it" / "done, stop silencing" pair is
// the shape that fits an agent call; reshaping a schedule is not.

func (h *handler) registerMaintenanceTools(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "list_maintenance_windows",
		Description: "Active and scheduled maintenance windows — planned-work silences that suppress alert " +
			"delivery (webhook/email) for a host, project, container, rule or severity without turning off " +
			"observation. Alert events still get recorded while a window is active; only their delivery is skipped.",
	}, h.listMaintenanceWindows)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "create_maintenance_window",
		Description: "Start a maintenance window: suppress alert delivery for the given scope, right now, for the " +
			"given duration. A reason is required — this silences paging, so why must always be answerable later. " +
			"Every scope field left empty means unrestricted on that dimension; leaving them ALL empty silences everything.",
	}, h.createMaintenanceWindow)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "end_maintenance_window",
		Description: "End a maintenance window early — the work finished ahead of schedule. Does not delete it.",
	}, h.endMaintenanceWindow)
}

type maintenanceWindowOut struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Reason     string   `json:"reason"`
	Author     string   `json:"author,omitempty"`
	HostIDs    []int64  `json:"hostIds,omitempty"`
	Project    string   `json:"project,omitempty"`
	Container  string   `json:"container,omitempty"`
	RuleID     *int64   `json:"ruleId,omitempty"`
	Severities []string `json:"severities,omitempty"`
	Recurring  bool     `json:"recurring"`
	StartsAt   string   `json:"startsAt"`
	EndsAt     string   `json:"endsAt,omitempty"`
	Active     bool     `json:"active"`
	Ended      bool     `json:"ended"`
}

func toMaintenanceWindowOut(w store.MaintenanceWindow) maintenanceWindowOut {
	out := maintenanceWindowOut{
		ID: w.ID, Name: w.Name, Reason: w.Reason, Author: w.Author,
		HostIDs: w.HostIDs, Project: w.Project, Container: w.Container, RuleID: w.RuleID, Severities: w.Severities,
		Recurring: w.Recurring, StartsAt: w.StartsAt.Format(time.RFC3339),
		Active: w.Active(time.Now()), Ended: w.Ended,
	}
	if !w.EndsAt.IsZero() {
		out.EndsAt = w.EndsAt.Format(time.RFC3339)
	}
	return out
}

// ---- list_maintenance_windows ----

type listMaintenanceWindowsOut struct {
	Windows []maintenanceWindowOut `json:"windows"`
}

func (h *handler) listMaintenanceWindows(ctx context.Context, req *mcpsdk.CallToolRequest, in struct{}) (*mcpsdk.CallToolResult, listMaintenanceWindowsOut, error) {
	p, err := h.authorize(ctx, req, "alerts", false, 0)
	if err != nil {
		return nil, listMaintenanceWindowsOut{}, err
	}
	all, err := h.deps.Store.ListMaintenanceWindows(ctx)
	if err != nil {
		return nil, listMaintenanceWindowsOut{}, err
	}
	out := listMaintenanceWindowsOut{Windows: []maintenanceWindowOut{}}
	for _, w := range all {
		// Same visibility rule as write access: a window naming a host outside
		// the caller's reach must not even be listed (leaks the host's name
		// and the window's reason otherwise).
		if h.maintenanceWindowHostsAllowed(ctx, p, w.HostIDs) {
			out.Windows = append(out.Windows, toMaintenanceWindowOut(w))
		}
	}
	return nil, out, nil
}

// ---- create_maintenance_window ----

type createMaintenanceWindowInput struct {
	Name        string   `json:"name" jsonschema:"a short label for this window"`
	Reason      string   `json:"reason" jsonschema:"why (required) — this suppresses paging, so it must stay explainable"`
	HostIDs     []int64  `json:"host_ids,omitempty" jsonschema:"restrict to these Docker host ids; empty = every host"`
	Project     string   `json:"project,omitempty" jsonschema:"restrict to a compose project (stack) name substring"`
	Container   string   `json:"container,omitempty" jsonschema:"restrict to a container name substring"`
	RuleID      *int64   `json:"rule_id,omitempty" jsonschema:"restrict to one alert rule id"`
	Severities  []string `json:"severities,omitempty" jsonschema:"restrict to these severities (info, warning, critical); empty = every severity"`
	DurationMin int      `json:"duration_min" jsonschema:"how long the window lasts, starting now, in minutes"`
}

func (h *handler) createMaintenanceWindow(ctx context.Context, req *mcpsdk.CallToolRequest, in createMaintenanceWindowInput) (*mcpsdk.CallToolResult, maintenanceWindowOut, error) {
	p, err := h.authorize(ctx, req, "alerts", true, 0)
	if err != nil {
		return nil, maintenanceWindowOut{}, err
	}
	if in.Name == "" {
		return nil, maintenanceWindowOut{}, errors.New("name is required")
	}
	if in.Reason == "" {
		return nil, maintenanceWindowOut{}, errors.New("reason is required")
	}
	if in.DurationMin <= 0 {
		return nil, maintenanceWindowOut{}, errors.New("duration_min must be positive")
	}
	if !h.maintenanceWindowHostsAllowed(ctx, p, in.HostIDs) {
		return nil, maintenanceWindowOut{}, errors.New("cannot scope a maintenance window to a host outside your access")
	}
	now := time.Now()
	win := &store.MaintenanceWindow{
		Name: in.Name, Reason: in.Reason, AuthorID: p.user.ID,
		HostIDs: in.HostIDs, Project: in.Project, Container: in.Container, RuleID: in.RuleID, Severities: in.Severities,
		StartsAt: now, EndsAt: now.Add(time.Duration(in.DurationMin) * time.Minute),
	}
	id, err := h.deps.Store.CreateMaintenanceWindow(ctx, win)
	if err != nil {
		return nil, maintenanceWindowOut{}, err
	}
	win.ID = id
	h.audit(p, "mcp.maintenance_window.create", in.Name, in.Reason)
	return nil, toMaintenanceWindowOut(*win), nil
}

// ---- end_maintenance_window ----

type endMaintenanceWindowInput struct {
	ID int64 `json:"id" jsonschema:"the maintenance window id, from list_maintenance_windows"`
}

type endMaintenanceWindowOut struct {
	OK bool `json:"ok"`
}

var errNoSuchMaintenanceWindow = errors.New("no such maintenance window, or it belongs to a host outside your access")

func (h *handler) endMaintenanceWindow(ctx context.Context, req *mcpsdk.CallToolRequest, in endMaintenanceWindowInput) (*mcpsdk.CallToolResult, endMaintenanceWindowOut, error) {
	p, err := h.authorize(ctx, req, "alerts", true, 0)
	if err != nil {
		return nil, endMaintenanceWindowOut{}, err
	}
	if in.ID <= 0 {
		return nil, endMaintenanceWindowOut{}, errors.New("id is required")
	}
	win, werr := h.deps.Store.MaintenanceWindowByID(ctx, in.ID)
	if werr != nil || !h.maintenanceWindowHostsAllowed(ctx, p, win.HostIDs) {
		return nil, endMaintenanceWindowOut{}, errNoSuchMaintenanceWindow
	}
	if err := h.deps.Store.EndMaintenanceWindow(ctx, in.ID); err != nil {
		return nil, endMaintenanceWindowOut{}, err
	}
	h.audit(p, "mcp.maintenance_window.end", strconv.FormatInt(in.ID, 10), "")
	return nil, endMaintenanceWindowOut{OK: true}, nil
}

// maintenanceWindowHostsAllowed is the MCP-side twin of the REST handler's
// check of the same name: a window naming a host outside the principal's
// reach must be neither creatable, visible, nor endable by them. Empty
// hostIDs ("every host") requires unrestricted host access.
func (h *handler) maintenanceWindowHostsAllowed(ctx context.Context, p *principal, hostIDs []int64) bool {
	if len(hostIDs) == 0 {
		_, all := h.scopedHostIDs(ctx, p)
		return all
	}
	for _, id := range hostIDs {
		if p.narrowed("alerts", false, id) != nil {
			return false
		}
		if h.deps.CheckAccess(ctx, p.user, "alerts", false, id) != nil {
			return false
		}
	}
	return true
}
