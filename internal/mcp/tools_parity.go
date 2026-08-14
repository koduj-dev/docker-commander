package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// Tools that close gaps between what the web UI can do and what MCP could see.
//
// Each of these already existed in the app and simply had no MCP surface, which
// left an assistant able to reason about a problem but not to finish the thought:
// it could restart a container but not the stack around it, read an alert but not
// whether anyone was actually told, and look at an image without being able to say
// whether it was full of known holes.

const scanMaxVulns = 100

// maxStackContainerIDs caps how many containers one restart_stack_containers /
// stop_stack_containers call may name.
//
// The per-user control rate limit (docs/mcp.md) caps CALLS per minute, on the
// assumption that one call is roughly one container — that is what turns "the
// whole estate stopped" into "a few containers stopped and the audit log is
// shouting". A generic bulk tool with no cap of its own would let a single
// call act on arbitrarily many containers, silently defeating that assumption
// without ever tripping the limiter. This constant is that missing cap: a
// named number so the value shows up once — in the tool description, the
// validator, and the error — rather than as a handful of matching magic 10s.
const maxStackContainerIDs = 10

// errNoSuchAlert covers both "no such id" and "belongs to a host you cannot
// reach". One message for both, so the tool cannot be used to discover which
// alert ids exist elsewhere.
var errNoSuchAlert = errors.New("no such alert, or it belongs to a host outside your access")

// mustAuthorize discards the principal so an authorize() call can be used as a
// plain predicate.
//
// Alerts are checked against the alert's host with the "alerts" section ALONE —
// deliberately not through authorizeHost, which additionally demands "hosts".
// That extra requirement is right for projects, where reaching a remote host
// means acting on it, and wrong here: a user whose alerts grant is scoped to a
// remote host already sees those alerts in the feed without holding "hosts", so
// demanding it would let them read an alert and then refuse to tell them whether
// anyone was paged about it.
func mustAuthorize(_ *principal, err error) error { return err }

func (h *handler) registerParityTools(s *mcpsdk.Server) {
	for _, a := range []struct{ name, action, verb string }{
		{"start_stack", "start", "Start"},
		{"stop_stack", "stop", "Stop"},
		{"restart_stack", "restart", "Restart"},
	} {
		mcpsdk.AddTool(s, &mcpsdk.Tool{
			Name: a.name,
			Description: a.verb + " every container in a Compose stack, by project name. " +
				"Affects the whole stack — prefer the per-container tools when one service is the problem.",
		}, h.stackActionTool(a.action))
	}

	for _, a := range []struct{ name, action, verb, whole string }{
		{"restart_stack_containers", "restart", "Restart", "restart_stack"},
		{"stop_stack_containers", "stop", "Stop", "stop_stack"},
	} {
		mcpsdk.AddTool(s, &mcpsdk.Tool{
			Name: a.name,
			Description: fmt.Sprintf(
				"%s a caller-chosen subset (up to %d) of one Compose stack's own containers, by project name and "+
					"container ids from list_containers. Every id is verified server-side to belong to that "+
					"project — if even one doesn't, the whole call is refused, nothing runs. Use %s to act on the "+
					"whole stack instead.",
				a.verb, maxStackContainerIDs, a.whole),
		}, h.stackContainersActionTool(a.action))
	}

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "scan_image",
		Description: "Scan an image for known vulnerabilities (Trivy) and return the severity summary with the " +
			"most serious findings. Reports whether Trivy is installed rather than failing when it is not.",
	}, h.scanImage)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "alert_delivery",
		Description: "Whether an alert actually reached anyone: every webhook call and e-mail send recorded against " +
			"it, with the outcome. An alert with no delivery attempts was never routed anywhere; a failed one means " +
			"nobody was told, which is different from nobody responding.",
	}, h.alertDelivery)
}

// ---- stack lifecycle ----

type stackActionInput struct {
	Project string `json:"project" jsonschema:"the Compose project (stack) name, from list_projects"`
	HostID  int64  `json:"host_id,omitempty" jsonschema:"Docker host id; 0 or omitted = the default local host"`
}

type stackActionOut struct {
	OK      bool   `json:"ok"`
	Project string `json:"project"`
	Action  string `json:"action"`
}

// stackActionTool builds the handler for one lifecycle verb.
//
// Deliberately only start/stop/restart. StackAction also implements "remove",
// which force-removes the stack's containers and its networks — that is
// destruction, not safe control, and it is not offered here for the same reason
// `prune` and `remove` are absent from the container tools.
func (h *handler) stackActionTool(action string) func(context.Context, *mcpsdk.CallToolRequest, stackActionInput) (*mcpsdk.CallToolResult, stackActionOut, error) {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in stackActionInput) (*mcpsdk.CallToolResult, stackActionOut, error) {
		p, err := h.authorize(ctx, req, "containers", true, in.HostID)
		if err != nil {
			return nil, stackActionOut{}, err
		}
		if strings.TrimSpace(in.Project) == "" {
			return nil, stackActionOut{}, errors.New("project is required")
		}
		derr := h.deps.Docker.StackAction(ctx, in.HostID, in.Project, action)
		// Audited whether it worked or not: an attempted stop that failed is
		// exactly the kind of thing someone will later want to find.
		h.audit(p, "mcp.stack."+action, in.Project, outcome(derr))
		if derr != nil {
			return nil, stackActionOut{}, derr
		}
		return nil, stackActionOut{OK: true, Project: in.Project, Action: action}, nil
	}
}

// ---- stack container subset lifecycle ----

type stackContainersActionInput struct {
	Project      string   `json:"project" jsonschema:"the Compose project (stack) name, from list_projects"`
	ContainerIDs []string `json:"container_ids" jsonschema:"full container IDs from list_containers, all belonging to project; up to 10 per call"`
	HostID       int64    `json:"host_id,omitempty" jsonschema:"Docker host id; 0 or omitted = the default local host"`
}

type stackContainersActionOut struct {
	OK      bool                      `json:"ok"`
	Project string                    `json:"project"`
	Action  string                    `json:"action"`
	Results []docker.BulkActionResult `json:"results"`
}

// validateStackContainerIDs enforces the container_ids shape before any
// Docker call is made: non-empty, and no more than maxStackContainerIDs. Kept
// as its own function so the cap boundary can be unit-tested directly,
// without a handler or a Docker daemon behind it.
func validateStackContainerIDs(ids []string) error {
	if len(ids) == 0 {
		return errors.New("container_ids is required")
	}
	if len(ids) > maxStackContainerIDs {
		return fmt.Errorf(
			"container_ids has %d entries; restart_stack_containers/stop_stack_containers are capped at %d per "+
				"call — use restart_stack/stop_stack to act on the whole stack instead",
			len(ids), maxStackContainerIDs)
	}
	return nil
}

// stackContainersActionTool builds the handler for one lifecycle verb, acting
// on a caller-chosen subset of one stack's own containers rather than the
// whole stack (stackActionTool) or a single container (containerActionTool).
//
// Deliberately only restart/stop, matching BulkStackContainerAction and the
// two sibling tools it sits between — this is the same "safe control"
// restriction, not a wider one, just narrower in scope-per-call than
// restart_stack/stop_stack and broader in count than restart_container/
// stop_container.
func (h *handler) stackContainersActionTool(action string) func(context.Context, *mcpsdk.CallToolRequest, stackContainersActionInput) (*mcpsdk.CallToolResult, stackContainersActionOut, error) {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in stackContainersActionInput) (*mcpsdk.CallToolResult, stackContainersActionOut, error) {
		p, err := h.authorize(ctx, req, "containers", true, in.HostID)
		if err != nil {
			return nil, stackContainersActionOut{}, err
		}
		if strings.TrimSpace(in.Project) == "" {
			return nil, stackContainersActionOut{}, errors.New("project is required")
		}
		if err := validateStackContainerIDs(in.ContainerIDs); err != nil {
			return nil, stackContainersActionOut{}, err
		}

		results, derr := h.deps.Docker.BulkStackContainerAction(ctx, in.HostID, in.Project, in.ContainerIDs, action)
		if derr != nil {
			// Refused before a single container was touched — an id that doesn't
			// belong to the stack, most likely. Audited the same way a failed
			// whole-stack action is: an attempt was made even though nothing ran,
			// and that is exactly the kind of thing worth finding later.
			h.audit(p, "mcp.stack."+action, in.Project, outcome(derr))
			return nil, stackContainersActionOut{}, derr
		}

		// Membership held, so every id was actually attempted.
		ok := h.auditStackContainerResults(p, action, results)
		return nil, stackContainersActionOut{OK: ok, Project: in.Project, Action: action, Results: results}, nil
	}
}

// auditStackContainerResults writes one audit entry per container result,
// success or failure, matching the granularity the REST bulk endpoint already
// established (internal/api/docker_handlers.go) and reusing the same
// mcp.container.<action> codes containerActionTool writes — no new audit code
// family for this tool. Returns whether every container in the batch
// succeeded.
//
// Split out from stackContainersActionTool so the audit fan-out can be
// exercised directly against a synthetic result set, without going through a
// live Docker daemon to produce a mixed success/failure batch.
func (h *handler) auditStackContainerResults(p *principal, action string, results []docker.BulkActionResult) bool {
	ok := true
	for _, r := range results {
		if r.OK {
			h.audit(p, "mcp.container."+action, r.ID, outcome(nil))
		} else {
			ok = false
			h.audit(p, "mcp.container."+action, r.ID, outcome(errors.New(r.Error)))
		}
	}
	return ok
}

// ---- scan_image ----

type scanImageInput struct {
	Ref    string `json:"ref" jsonschema:"image reference, e.g. nginx:1.27"`
	HostID int64  `json:"host_id,omitempty" jsonschema:"Docker host id; 0 or omitted = the default local host"`
}

type scanImageOut struct {
	Available bool           `json:"available"`
	Ref       string         `json:"ref,omitempty"`
	Summary   map[string]int `json:"summary,omitempty"`
	Vulns     []vulnBrief    `json:"vulns,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type vulnBrief struct {
	ID           string `json:"id"`
	Severity     string `json:"severity"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	FixedVersion string `json:"fixedVersion,omitempty"`
	Title        string `json:"title,omitempty"`
}

func (h *handler) scanImage(ctx context.Context, req *mcpsdk.CallToolRequest, in scanImageInput) (*mcpsdk.CallToolResult, scanImageOut, error) {
	// A scan is gated as a WRITE, matching the REST route. It shells out to Trivy
	// and pulls the image if absent, so it is real work on the host rather than a
	// lookup — a read-only token must not be able to trigger it.
	if _, err := h.authorize(ctx, req, "images", true, in.HostID); err != nil {
		return nil, scanImageOut{}, err
	}
	if !docker.ValidImageRef(in.Ref) {
		// The same guard the REST handler applies: the ref reaches a CLI argument,
		// and a leading '-' would be read as a flag.
		return nil, scanImageOut{}, errors.New("invalid image reference")
	}
	if !docker.TrivyAvailable(ctx) {
		return nil, scanImageOut{
			Available: false,
			Error:     "Trivy is not installed on the host running Docker Commander",
		}, nil
	}

	var env []string
	cleanup := func() {}
	if in.HostID != 0 {
		host, err := h.deps.Store.HostByID(ctx, in.HostID)
		if err != nil {
			return nil, scanImageOut{}, errors.New("unknown host")
		}
		env, cleanup, err = docker.ComposeHostEnv(host)
		if err != nil {
			return nil, scanImageOut{Available: true, Error: err.Error()}, nil
		}
	}
	defer cleanup()

	res, err := docker.ScanImage(ctx, env, in.Ref)
	if err != nil {
		return nil, scanImageOut{Available: true, Error: err.Error()}, nil
	}

	out := scanImageOut{Available: true, Ref: res.Ref, Summary: res.Summary, Vulns: []vulnBrief{}}
	// The summary carries the full counts; the list is capped because a base image
	// can carry thousands of CVEs and the model needs the shape, not the census.
	for i, v := range res.Vulns {
		if i >= scanMaxVulns {
			out.Truncated = true
			break
		}
		out.Vulns = append(out.Vulns, vulnBrief{
			ID: v.ID, Severity: v.Severity, Package: v.Package,
			Version: v.Version, FixedVersion: v.FixedVersion, Title: v.Title,
		})
	}
	return nil, out, nil
}

// ---- alert_delivery ----

type alertDeliveryInput struct {
	AlertID int64 `json:"alert_id" jsonschema:"the alert event id, from list_alerts"`
}

type deliveryBrief struct {
	Channel   string `json:"channel"`
	Target    string `json:"target"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Attempted string `json:"attemptedAt"`
}

type alertDeliveryOut struct {
	Attempts []deliveryBrief `json:"attempts"`
}

func (h *handler) alertDelivery(ctx context.Context, req *mcpsdk.CallToolRequest, in alertDeliveryInput) (*mcpsdk.CallToolResult, alertDeliveryOut, error) {
	if _, err := h.authorize(ctx, req, "alerts", false, 0); err != nil {
		return nil, alertDeliveryOut{}, err
	}
	if in.AlertID <= 0 {
		return nil, alertDeliveryOut{}, errors.New("alert_id is required")
	}
	// Authorise against the host the alert belongs to, not host 0. list_alerts
	// scopes its results by host; without the same check here, a token confined
	// to one host could walk alert ids — they are sequential integers — and read
	// back which webhooks fired for another host and whether they succeeded.
	// The REST feed never has this problem because it only ever fetches
	// deliveries for events a host-scoped query already returned.
	//
	// A missing alert and an out-of-reach one deliberately give the SAME answer:
	// distinguishing them would turn this into an oracle for which ids exist on
	// hosts the caller cannot see.
	hostID, herr := h.deps.Store.AlertEventHost(ctx, in.AlertID)
	if herr != nil || mustAuthorize(h.authorize(ctx, req, "alerts", false, hostID)) != nil {
		return nil, alertDeliveryOut{}, errNoSuchAlert
	}
	byEvent, err := h.deps.Store.AlertDeliveriesFor(ctx, []int64{in.AlertID})
	if err != nil {
		return nil, alertDeliveryOut{}, err
	}
	out := alertDeliveryOut{Attempts: []deliveryBrief{}}
	for _, d := range byEvent[in.AlertID] {
		// Target is already the webhook's NAME and host rather than its URL, and
		// detail is a truncated response excerpt — both shaped that way at the
		// point of recording, precisely because a webhook URL carries a token.
		out.Attempts = append(out.Attempts, deliveryBrief{
			Channel: d.Channel, Target: d.Target, OK: d.OK,
			Status: d.Status, Detail: d.Detail,
			Attempted: d.Attempted.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return nil, out, nil
}

// ---- preview_deploy ----

type previewDeployInput struct {
	ProjectID int64 `json:"project_id" jsonschema:"the managed project id, from list_managed_projects"`
}

func (h *handler) registerPreviewTool(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "preview_deploy",
		Description: "What deploying a managed project WOULD change, without deploying it: which services would be " +
			"created, which would be recreated with a different image, and which are running but no longer in the " +
			"compose file. Also reports an invalid compose file. Use this before deploy_project.",
	}, h.previewDeploy)
}

func (h *handler) previewDeploy(ctx context.Context, req *mcpsdk.CallToolRequest, in previewDeployInput) (*mcpsdk.CallToolResult, ProjectPreview, error) {
	// A read: it resolves the compose file and lists containers, and changes
	// nothing. Gating it as a write would be the wrong lesson — a preview must be
	// cheaper to reach than the deploy it protects, or nobody uses it.
	if _, err := h.authorize(ctx, req, "projects", false, 0); err != nil {
		return nil, ProjectPreview{}, err
	}
	if h.deps.PreviewProject == nil {
		return nil, ProjectPreview{}, errors.New("project preview is not available on this server")
	}
	if in.ProjectID <= 0 {
		return nil, ProjectPreview{}, errors.New("project_id is required")
	}
	// Re-check against the host the project actually targets, exactly as
	// deploy/down do. Being a read does not exempt it: a preview lists the
	// services and images running on that host, which is precisely the thing the
	// per-host scope exists to withhold. Until remote projects were allowed, the
	// blanket refusal of them WAS this check — removing that refusal without
	// putting this here is what turned a closed door into an open one.
	if err := h.authorizeProjectHost(ctx, req, in.ProjectID, false); err != nil {
		return nil, ProjectPreview{}, err
	}
	out, err := h.deps.PreviewProject(ctx, in.ProjectID)
	if err != nil {
		return nil, ProjectPreview{}, err
	}
	return nil, out, nil
}
