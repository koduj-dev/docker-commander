package mcp

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// Diagnostic tools.
//
// These exist to answer the questions that otherwise create pressure to open
// `exec`: what is actually running inside this container, what has changed on its
// filesystem, and where across the whole host did a particular error appear.
// All three are read-only and bounded, and none of them can run anything — which
// is the point. A shell would answer the same questions and a great many others
// nobody intended to allow.

const (
	searchDefaultTail = 500
	searchMaxTail     = 2000
	searchMaxMatches  = 200
	searchMaxPattern  = 200
	diffMaxEntries    = 500
)

func (h *handler) registerDiagnosticTools(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "container_processes",
		Description: "The processes running inside a container (docker top). Read-only: it reports what is running, " +
			"it cannot start or stop anything.",
	}, h.containerProcesses)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "container_changes",
		Description: "Files added, modified or deleted in a container's writable layer since it started (docker diff). " +
			"Useful for spotting configuration written at runtime, or a process filling the layer. Paths only — no contents.",
	}, h.containerChanges)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "search_logs",
		Description: "Search recent log lines ACROSS containers on a host for a substring or regular expression. " +
			"Use this to find where an error appears when you don't already know which container to look at; " +
			"container_logs is for reading one container you have already identified.",
	}, h.searchLogs)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "list_alert_rules",
		Description: "The configured alert rules with their thresholds. An alert message like 'MEM 61% of limit > 5%' " +
			"cannot be judged without the rule behind it — this is how to tell a real problem from a badly chosen threshold.",
	}, h.listAlertRules)
}

// ---- container_processes ----

type containerTargetInput struct {
	ContainerID string `json:"container_id" jsonschema:"container ID or name"`
	HostID      int64  `json:"host_id,omitempty" jsonschema:"Docker host id; 0 or omitted = the default local host"`
}

type processesOut struct {
	Titles    []string   `json:"titles"`
	Processes [][]string `json:"processes"`
}

func (h *handler) containerProcesses(ctx context.Context, req *mcpsdk.CallToolRequest, in containerTargetInput) (*mcpsdk.CallToolResult, processesOut, error) {
	if _, err := h.authorize(ctx, req, "containers", false, in.HostID); err != nil {
		return nil, processesOut{}, err
	}
	top, err := h.deps.Docker.ContainerTop(ctx, in.HostID, in.ContainerID)
	if err != nil {
		return nil, processesOut{}, err
	}
	return nil, processesOut{Titles: top.Titles, Processes: top.Processes}, nil
}

// ---- container_changes ----

type changesOut struct {
	Changes   []docker.DiffEntry `json:"changes"`
	Truncated bool               `json:"truncated,omitempty"`
}

func (h *handler) containerChanges(ctx context.Context, req *mcpsdk.CallToolRequest, in containerTargetInput) (*mcpsdk.CallToolResult, changesOut, error) {
	if _, err := h.authorize(ctx, req, "containers", false, in.HostID); err != nil {
		return nil, changesOut{}, err
	}
	changes, err := h.deps.Docker.ContainerDiff(ctx, in.HostID, in.ContainerID)
	if err != nil {
		return nil, changesOut{}, err
	}
	out := changesOut{Changes: changes}
	// A busy container can have tens of thousands of changed paths, which is
	// neither useful to a model nor kind to its context window.
	if len(out.Changes) > diffMaxEntries {
		out.Changes = out.Changes[:diffMaxEntries]
		out.Truncated = true
	}
	if out.Changes == nil {
		out.Changes = []docker.DiffEntry{}
	}
	return nil, out, nil
}

// ---- search_logs ----

type searchLogsInput struct {
	Pattern   string `json:"pattern" jsonschema:"text to find, or a regular expression when regex is true"`
	Regex     bool   `json:"regex,omitempty" jsonschema:"treat pattern as a regular expression"`
	Container string `json:"container,omitempty" jsonschema:"only search containers whose name contains this"`
	HostID    int64  `json:"host_id,omitempty" jsonschema:"Docker host id; 0 or omitted = the default local host"`
	Tail      int    `json:"tail,omitempty" jsonschema:"lines to examine per container (default 500, max 2000)"`
}

type logMatch struct {
	Container string `json:"container"`
	Stream    string `json:"stream"`
	Time      string `json:"time,omitempty"`
	Message   string `json:"message"`
}

type searchLogsOut struct {
	Matches   []logMatch `json:"matches"`
	Searched  int        `json:"containersSearched"`
	Truncated bool       `json:"truncated,omitempty"`
}

func (h *handler) searchLogs(ctx context.Context, req *mcpsdk.CallToolRequest, in searchLogsInput) (*mcpsdk.CallToolResult, searchLogsOut, error) {
	if _, err := h.authorize(ctx, req, "logs", false, in.HostID); err != nil {
		return nil, searchLogsOut{}, err
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return nil, searchLogsOut{}, errEmptyPattern
	}
	if len(in.Pattern) > searchMaxPattern {
		return nil, searchLogsOut{}, errPatternTooLong
	}

	var re *regexp.Regexp
	if in.Regex {
		// Compiled with a size limit and matched against bounded input; a bad
		// pattern is a user error, not a way to hang the server.
		compiled, err := regexp.Compile(in.Pattern)
		if err != nil {
			return nil, searchLogsOut{}, err
		}
		re = compiled
	}
	needle := strings.ToLower(in.Pattern)

	tail := in.Tail
	if tail <= 0 {
		tail = searchDefaultTail
	}
	if tail > searchMaxTail {
		tail = searchMaxTail
	}

	containers, err := h.deps.Docker.ListContainers(ctx, in.HostID)
	if err != nil {
		return nil, searchLogsOut{}, err
	}

	out := searchLogsOut{Matches: []logMatch{}}
	for _, c := range containers {
		if c.State != "running" {
			continue // a stopped container's logs are still readable one by one
		}
		if in.Container != "" && !strings.Contains(strings.ToLower(c.Name), strings.ToLower(in.Container)) {
			continue
		}
		if len(out.Matches) >= searchMaxMatches {
			out.Truncated = true
			break
		}
		out.Searched++

		var mu sync.Mutex
		name := c.Name
		// StreamLogs emits from two goroutines (stdout + stderr), so the callback
		// serialises access to the shared result.
		_ = h.deps.Docker.StreamLogs(ctx, in.HostID, c.ID, false, strconv.Itoa(tail), func(l docker.LogLine) {
			mu.Lock()
			defer mu.Unlock()
			if len(out.Matches) >= searchMaxMatches {
				out.Truncated = true
				return
			}
			hit := false
			if re != nil {
				hit = re.MatchString(l.Message)
			} else {
				hit = strings.Contains(strings.ToLower(l.Message), needle)
			}
			if !hit {
				return
			}
			out.Matches = append(out.Matches, logMatch{
				Container: name, Stream: l.Stream, Time: l.Timestamp, Message: l.Message,
			})
		})
		// A container whose logs can't be read is skipped rather than failing the
		// whole search: one broken container should not hide the other twenty.
	}
	return nil, out, nil
}

// ---- list_alert_rules ----

type listAlertRulesInput struct {
	OnlyEnabled bool `json:"only_enabled,omitempty" jsonschema:"skip disabled rules"`
}

type alertRuleBrief struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Type        string `json:"type"`
	Target      string `json:"target,omitempty"`
	Severity    string `json:"severity"`
	Config      string `json:"config"`
	CooldownSec int    `json:"cooldownSec"`
	Notifies    string `json:"notifies,omitempty"`
}

type listAlertRulesOut struct {
	Rules []alertRuleBrief `json:"rules"`
}

func (h *handler) listAlertRules(ctx context.Context, req *mcpsdk.CallToolRequest, in listAlertRulesInput) (*mcpsdk.CallToolResult, listAlertRulesOut, error) {
	if _, err := h.authorize(ctx, req, "alerts", false, 0); err != nil {
		return nil, listAlertRulesOut{}, err
	}
	rules, err := h.deps.Store.ListAlertRules(ctx)
	if err != nil {
		return nil, listAlertRulesOut{}, err
	}
	out := listAlertRulesOut{Rules: []alertRuleBrief{}}
	for _, r := range rules {
		if in.OnlyEnabled && !r.Enabled {
			continue
		}
		// Which channels it uses, but NOT the recipients or the webhook URL —
		// those are delivery configuration, and a model has no need for either to
		// interpret an alert.
		var ch []string
		if r.WebhookID != nil {
			ch = append(ch, "webhook")
		}
		if r.Email {
			ch = append(ch, "email")
		}
		out.Rules = append(out.Rules, alertRuleBrief{
			ID: r.ID, Name: r.Name, Enabled: r.Enabled, Type: r.Type,
			Target: r.Target, Severity: r.Severity, Config: r.Config,
			CooldownSec: r.CooldownSec, Notifies: strings.Join(ch, ","),
		})
	}
	return nil, out, nil
}

// errors returned to the caller as tool errors.
var (
	errEmptyPattern   = errStr("pattern is required")
	errPatternTooLong = errStr("pattern is too long")
)

type errStr string

func (e errStr) Error() string { return string(e) }
