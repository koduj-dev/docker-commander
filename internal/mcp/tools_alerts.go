package mcp

import (
	"context"
	"errors"
	"strconv"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Alerting tools.
//
// The engine tracks a threshold alert as a CONDITION with a lifetime rather than
// a line reprinted every cycle, and that distinction is the reason these are two
// separate tools instead of one:
//
//   - list_alerts answers "what happened" — the history, including conditions
//     that have since resolved.
//   - active_alert_conditions answers "what is wrong right now" — the live set,
//     which is a much smaller and more decisive answer to hand a model that is
//     trying to diagnose something.
//
// Asking the first question when you meant the second is how an assistant ends up
// reporting a problem that fixed itself an hour ago.

const (
	alertsDefaultLimit = 50
	alertsMaxLimit     = 200
)

func (h *handler) registerAlertTools(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "list_alerts",
		Description: "Alert history, newest first. Filter by severity (info|warning|critical), " +
			"lifecycle kind (firing|escalated|eased|repeat|resolved), container, rule or message text. " +
			"A 'resolved' entry means the condition ended — check the kind before reporting a problem as current.",
	}, h.listAlerts)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "active_alert_conditions",
		Description: "Conditions currently over threshold: what is wrong RIGHT NOW, with how long each has been going. " +
			"Prefer this over list_alerts when diagnosing — it excludes anything that has already resolved.",
	}, h.activeAlertConditions)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "acknowledge_alert",
		Description: "Mark one alert acknowledged, recording who did it. Does not change anything about the container " +
			"or the condition — it only records that a human has seen it.",
	}, h.acknowledgeAlert)
}

// ---- list_alerts ----

type listAlertsInput struct {
	Severity  string `json:"severity,omitempty" jsonschema:"info, warning or critical"`
	Kind      string `json:"kind,omitempty" jsonschema:"firing, escalated, eased, repeat or resolved"`
	Container string `json:"container,omitempty" jsonschema:"container name contains this"`
	Rule      string `json:"rule,omitempty" jsonschema:"rule name contains this"`
	Text      string `json:"text,omitempty" jsonschema:"alert message contains this"`
	HostID    int64  `json:"host_id,omitempty" jsonschema:"Docker host id; 0 or omitted = the default local host"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many to return (default 50, max 200)"`
}

type alertBrief struct {
	Time           string   `json:"time"`
	Kind           string   `json:"kind"`
	Severity       string   `json:"severity"`
	Rule           string   `json:"rule"`
	Host           string   `json:"host,omitempty"`
	Container      string   `json:"container,omitempty"`
	Message        string   `json:"message"`
	Value          *float64 `json:"value,omitempty"`
	DurationSec    int      `json:"durationSec,omitempty"`
	Acknowledged   bool     `json:"acknowledged"`
	AcknowledgedBy string   `json:"acknowledgedBy,omitempty"`
	ID             int64    `json:"id"`
}

type listAlertsOut struct {
	Alerts []alertBrief `json:"alerts"`
	Total  int          `json:"total"`
}

func (h *handler) listAlerts(ctx context.Context, req *mcpsdk.CallToolRequest, in listAlertsInput) (*mcpsdk.CallToolResult, listAlertsOut, error) {
	p, err := h.authorize(ctx, req, "alerts", false, in.HostID)
	if err != nil {
		return nil, listAlertsOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = alertsDefaultLimit
	}
	if limit > alertsMaxLimit {
		limit = alertsMaxLimit
	}

	q := store.AlertQuery{
		Severity: in.Severity, Kind: in.Kind, Container: in.Container,
		Rule: in.Rule, Text: in.Text, Limit: limit,
	}
	// Scope goes INTO the query. Without it, omitting host_id would hand back
	// every host's alerts regardless of what this principal may reach.
	if ids, all := h.scopedHostIDs(ctx, p); !all {
		q.HostIDs = ids
	}
	if in.HostID != 0 {
		id := in.HostID
		q.HostID = &id
	}
	events, total, err := h.deps.Store.ListAlertEvents(ctx, q)
	if err != nil {
		return nil, listAlertsOut{}, err
	}

	out := listAlertsOut{Alerts: []alertBrief{}, Total: total}
	for _, e := range events {
		out.Alerts = append(out.Alerts, alertBrief{
			ID: e.ID, Time: e.CreatedAt.Format(time.RFC3339),
			Kind: e.Kind, Severity: e.Severity, Rule: e.RuleName,
			Host: e.HostName, Container: e.ContainerName, Message: e.Message,
			Value: e.Value, DurationSec: e.DurationSec,
			Acknowledged: e.Acknowledged, AcknowledgedBy: e.AcknowledgedBy,
		})
	}
	return nil, out, nil
}

// ---- active_alert_conditions ----

type activeConditionsInput struct {
	HostID int64 `json:"host_id,omitempty" jsonschema:"Docker host id; 0 or omitted = the default local host"`
}

type activeCondition struct {
	Host      string   `json:"host,omitempty"`
	Container string   `json:"container"`
	Metric    string   `json:"metric"`
	Severity  string   `json:"severity"`
	Rule      string   `json:"rule"`
	Value     *float64 `json:"value,omitempty"`
	Since     string   `json:"since"`
	ForSec    int      `json:"forSec"`
}

type activeConditionsOut struct {
	Conditions []activeCondition `json:"conditions"`
}

func (h *handler) activeAlertConditions(ctx context.Context, req *mcpsdk.CallToolRequest, in activeConditionsInput) (*mcpsdk.CallToolResult, activeConditionsOut, error) {
	p, err := h.authorize(ctx, req, "alerts", false, in.HostID)
	if err != nil {
		return nil, activeConditionsOut{}, err
	}

	states, err := h.deps.Store.ListAlertStates(ctx)
	if err != nil {
		return nil, activeConditionsOut{}, err
	}
	now := time.Now()
	out := activeConditionsOut{Conditions: []activeCondition{}}
	for _, st := range states {
		// The stored set is engine-wide, so each row is re-checked against the
		// caller's host scope. Without this a token scoped to one host would learn
		// what is failing on the others.
		if err := p.narrowed("alerts", false, st.HostID); err != nil {
			continue
		}
		if err := h.deps.CheckAccess(ctx, p.user, "alerts", false, st.HostID); err != nil {
			continue
		}
		if in.HostID != 0 && st.HostID != in.HostID {
			continue
		}
		out.Conditions = append(out.Conditions, activeCondition{
			Host: st.HostName, Container: st.ContainerName, Metric: st.Metric,
			Severity: st.Severity, Rule: st.RuleName, Value: st.LastValue,
			Since: st.StartedAt.Format(time.RFC3339), ForSec: int(now.Sub(st.StartedAt).Seconds()),
		})
	}
	return nil, out, nil
}

// ---- acknowledge_alert ----

type ackAlertInput struct {
	ID int64 `json:"id" jsonschema:"the alert event id, from list_alerts"`
}

type ackAlertOut struct {
	OK bool `json:"ok"`
}

func (h *handler) acknowledgeAlert(ctx context.Context, req *mcpsdk.CallToolRequest, in ackAlertInput) (*mcpsdk.CallToolResult, ackAlertOut, error) {
	// A write, so a read-only user or a read-only token is refused — even though
	// nothing about the container changes. Acknowledging is a claim that somebody
	// looked, and it is attributed; a read-only principal must not be able to make
	// that claim on someone's behalf.
	p, err := h.authorize(ctx, req, "alerts", true, 0)
	if err != nil {
		return nil, ackAlertOut{}, err
	}
	if in.ID <= 0 {
		return nil, ackAlertOut{}, errors.New("id is required")
	}
	// Scoped to the alert's own host, matching the REST route. Same answer for a
	// missing alert and an out-of-reach one, so this cannot be used to discover
	// which ids exist elsewhere.
	hostID, herr := h.deps.Store.AlertEventHost(ctx, in.ID)
	if herr != nil || mustAuthorize(h.authorize(ctx, req, "alerts", true, hostID)) != nil {
		return nil, ackAlertOut{}, errNoSuchAlert
	}
	if err := h.deps.Store.AckAlertEvent(ctx, in.ID, p.user.Username); err != nil {
		return nil, ackAlertOut{}, err
	}
	h.audit(p, "mcp.alert.ack", strconv.FormatInt(in.ID, 10), "")
	return nil, ackAlertOut{OK: true}, nil
}

// scopedHostIDs returns the host ids this principal may read, and whether that
// is "all of them".
//
// Needed because an aggregate query cannot be authorised by checking one
// host_id: with no host argument, list_alerts would otherwise return events from
// every host to a token scoped to one. The REST feed pushes the same set into the
// SQL query rather than filtering afterwards, and for the same reason — dropping
// rows after the fact yields short pages and a total that counts what the caller
// may not see.
//
// Both constraints apply: the user's own reach AND the token's narrowing, since a
// token can only ever reduce its owner's rights.
func (h *handler) scopedHostIDs(ctx context.Context, p *principal) ([]int64, bool) {
	// Admins first: they bypass roles entirely, so they have no grants and
	// ReachableHosts would report "no hosts" for them — which would have hidden
	// every remote host's alerts from the one principal allowed to see all of
	// them. The REST side checks this before ReachableHosts for the same reason.
	if p.user != nil && p.user.IsAdmin() {
		if len(p.hosts) == 0 {
			return nil, true
		}
		return append([]int64{0}, p.hosts...), false
	}

	hosts, all, err := h.deps.Store.ReachableHosts(ctx, p.user)
	if err != nil {
		return []int64{0}, false // fail closed: the local daemon only
	}

	var ids []int64
	if all {
		if len(p.hosts) == 0 {
			return nil, true // unrestricted by either
		}
		// The token narrows an otherwise-unlimited user.
		ids = append(ids, 0)
		ids = append(ids, p.hosts...)
		return ids, false
	}

	ids = append(ids, 0) // the local daemon is always in reach
	for id := range hosts {
		if len(p.hosts) > 0 && !containsID(p.hosts, id) {
			continue // outside the token's narrowing
		}
		ids = append(ids, id)
	}
	return ids, false
}
