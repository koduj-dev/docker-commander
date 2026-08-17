package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/koduj-dev/docker-commander/internal/monitor"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list hosts")
		return
	}
	// Annotate each host with the monitor's live reachability. A host the
	// monitor hasn't probed yet (or a disabled one it skips) is reported
	// reachable so the UI doesn't flash a false "offline".
	health := s.monitor.HostHealth()
	// Hosts outside the caller's scope are not listed: the list carries names and
	// addresses, and showing a host you can neither reach nor act on is the leak
	// per-host scoping exists to prevent.
	visible := s.visibleHosts(r)
	// Shape a safe view; never leak TLS key material to the client.
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		if !visible(h.ID) {
			continue
		}
		row := map[string]any{
			"id": h.ID, "name": h.Name, "kind": h.Kind, "address": h.Address,
			"alertEmail": h.AlertEmail, "disabled": h.Disabled,
			"reachable": true,
		}
		if hh, ok := health[h.ID]; ok {
			row["reachable"] = hh.Reachable
			if !hh.Reachable {
				// Only the fact + since-time are surfaced; the raw ping error can
				// carry internal network/auth detail, so it stays server-side
				// (it's already in the monitor's logs).
				row["unreachableSince"] = hh.Since.Format(time.RFC3339)
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	list, err := s.docker.ListContainers(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleInspectContainer(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	detail, err := s.docker.InspectContainer(r.Context(), hostID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")
	if err := s.docker.ContainerAction(r.Context(), hostID, id, action); err != nil {
		if err == docker.ErrUnknownAction {
			writeErr(w, http.StatusBadRequest, "unknown action")
			return
		}
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "container."+action, id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// maxBulkContainerIDs caps how many containers one bulk-action request may
// name, so a single request can't be used to queue an unbounded amount of
// daemon work (the concurrency bound in BulkContainerAction limits how many
// run *at once*, not how many get queued in total).
const maxBulkContainerIDs = 200

// bulkContainerActions are the only actions the bulk endpoint accepts.
// Deliberately narrower than ContainerAction's full set (pause/unpause/kill
// also exist there): this endpoint is a deliberately scoped cut of bulk
// operations (see NEXT.md), and a generic action-agnostic bulk endpoint would
// let a future caller fire a destructive action across a whole selection
// without that being its own reviewed decision.
var bulkContainerActions = map[string]bool{"restart": true, "stop": true, "start": true}

// handleBulkContainerAction restarts or stops a set of containers, running the
// per-container calls with bounded parallelism and reporting one result per
// container rather than a single aggregate ok/fail.
func (s *Server) handleBulkContainerAction(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	var req struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !bulkContainerActions[req.Action] {
		writeErr(w, http.StatusBadRequest, "action must be restart, stop, or start")
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids is required")
		return
	}
	if len(req.IDs) > maxBulkContainerIDs {
		writeErr(w, http.StatusBadRequest, "too many containers in one request")
		return
	}

	results, err := s.docker.BulkContainerAction(r.Context(), hostID, req.IDs, req.Action)
	if err != nil {
		// A duplicate id — refused before any container was touched.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	succeeded, failed := 0, 0
	for _, res := range results {
		// One audit entry per container acted on, success or failure — a
		// bulk write that silently drops failed attempts from the audit log
		// would hide exactly the case an operator most needs a record of
		// (wrong/stale ids, an attempt against a container the caller
		// shouldn't have been able to target). The detail carries the
		// daemon's error text for failures, same convention as
		// stack.redeploy.failed.
		if res.OK {
			succeeded++
			s.audit(r, "container."+req.Action, res.ID, "")
		} else {
			failed++
			s.audit(r, "container."+req.Action, res.ID, res.Error)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results":   results,
		"succeeded": succeeded,
		"failed":    failed,
	})
}

// bulkPullFrame is one JSON message sent over the /containers/bulk-pull
// WebSocket. Pulling more than one image over a single connection needs
// every frame to say which image it's about — unlike single-image pull
// (/images/pull), which needs no such tag because there's only ever one.
type bulkPullFrame struct {
	Ref      string               `json:"ref"`
	Index    int                  `json:"index,omitempty"` // 1-based position among the images being pulled
	Count    int                  `json:"count,omitempty"` // total distinct images
	Started  bool                 `json:"started,omitempty"`
	Progress *docker.PullProgress `json:"progress,omitempty"`
	RefDone  bool                 `json:"refDone,omitempty"`
	OK       bool                 `json:"ok,omitempty"`
	Error    string               `json:"error,omitempty"`
	AllDone  bool                 `json:"allDone,omitempty"`
	Results  []bulkPullResult     `json:"results,omitempty"`
}

// bulkPullResult is one image's final outcome, plus which of the caller's
// containers run it — so the UI can report success/failure per container
// even though the pull itself ran once per distinct image.
type bulkPullResult struct {
	Ref          string   `json:"ref"`
	OK           bool     `json:"ok"`
	Error        string   `json:"error,omitempty"`
	ContainerIDs []string `json:"containerIds"`
}

// normalizeImageRef makes dedup-by-image robust to the same image being
// spelled with or without an explicit tag — a container created from "nginx"
// and one created from "nginx:latest" run the identical image, and without
// this they'd be treated as two distinct images to pull. Mirrors Docker's own
// "no tag or digest means :latest" default. Only the last path segment is
// checked for a ':', so a registry host:port prefix (e.g.
// "localhost:5000/nginx") is never mistaken for a tag.
func normalizeImageRef(ref string) string {
	if strings.Contains(ref, "@") {
		return ref // digest reference; no default tag applies
	}
	last := ref
	if i := strings.LastIndexByte(ref, '/'); i >= 0 {
		last = ref[i+1:]
	}
	if strings.Contains(last, ":") {
		return ref // already has an explicit tag
	}
	return ref + ":latest"
}

// bulkPullRequest is the first — and only — message the client sends on the
// /containers/bulk-pull WebSocket. Container ids travel here rather than in
// the connection URL because up to maxBulkContainerIDs full 64-char ids would
// not reliably fit in the request line/headers many reverse proxies cap (a
// few KiB), which would fail the WS handshake itself before this handler
// ever ran.
type bulkPullRequest struct {
	IDs []string `json:"ids"`
}

// maxBulkPullRequestBytes bounds the first message so an oversized payload is
// rejected outright rather than accepted up to some implicit library default.
// maxBulkContainerIDs (200) full ids plus JSON overhead comfortably fits.
const maxBulkPullRequestBytes = 32 * 1024

// handleBulkPullImages resolves a caller-chosen set of containers to the
// images they currently run, pulls each DISTINCT image once — containers
// sharing an image are not pulled twice — and streams progress for each over
// one WebSocket connection, mirroring handlePullImage's per-layer frames.
//
// The caller names containers, never a raw image reference: every id is
// verified against the host's real container list before anything is
// pulled, and an id that doesn't match one refuses the whole request. That
// keeps WHICH images can be named scoped to the "containers" section (see
// sectionForPath) the permissions middleware already gates this route by —
// but the pull itself is squarely an images-subsystem operation: it attaches
// a stored registry credential (see PullImage/authForRef) and mutates the
// shared local image store, the same capability /images/pull requires
// "images" write for. A containers-only role must not gain that just because
// this route happens to hang off /containers — so on top of the middleware's
// "containers" check, this handler independently requires "images" write
// too, the same way the middleware itself would for any other route.
func (s *Server) handleBulkPullImages(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	u, err := s.store.UserByID(r.Context(), claims.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.checkAccess(r.Context(), u, "images", true, hostID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}

	opts := &websocket.AcceptOptions{}
	if s.cfg.Dev {
		opts.InsecureSkipVerify = true
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxBulkPullRequestBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	send := func(f bulkPullFrame) {
		b, err := json.Marshal(f)
		if err != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			// The client is gone (closed the tab, hit Cancel): cancel ctx so
			// the in-flight PullImage call (which uses this same ctx for its
			// daemon request) aborts instead of running the rest of the
			// batch to completion with no one left to report it to.
			cancel()
		}
	}
	fail := func(msg string) {
		send(bulkPullFrame{AllDone: true, Error: msg})
	}

	// A 10s deadline on the init message alone — a client that connects and
	// never sends its ids must not hang this goroutine forever.
	initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := conn.Read(initCtx)
	initCancel()
	if err != nil {
		return
	}
	var req bulkPullRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fail("invalid request")
		return
	}
	ids := req.IDs
	if len(ids) == 0 {
		fail("ids is required")
		return
	}
	if len(ids) > maxBulkContainerIDs {
		fail("too many containers in one request")
		return
	}
	if dup := docker.FirstDuplicateID(ids); dup != "" {
		fail(fmt.Sprintf("container %q is listed more than once in the same request", dup))
		return
	}

	containers, err := s.docker.ListContainers(ctx, hostID)
	if err != nil {
		fail("docker error: " + err.Error())
		return
	}
	imageByID := make(map[string]string, len(containers))
	for _, c := range containers {
		imageByID[c.ID] = c.Image
	}

	// Resolve every id to its image, and dedupe by image, before touching
	// anything — fail closed on an unknown id the same way
	// BulkStackContainerAction's membership check does, rather than silently
	// skipping it.
	var refs []string
	containersByRef := make(map[string][]string, len(ids))
	for _, id := range ids {
		image, ok := imageByID[id]
		if !ok {
			fail(fmt.Sprintf("container %q not found", id))
			return
		}
		ref := normalizeImageRef(image)
		if _, seen := containersByRef[ref]; !seen {
			refs = append(refs, ref)
		}
		containersByRef[ref] = append(containersByRef[ref], id)
	}

	// Watch for the client going away (Cancel, tab closed) so a disconnect is
	// noticed as soon as it happens rather than only on the next progress
	// write — a pull with sparse progress ticks could otherwise run to
	// completion server-side well after the user asked to stop. No further
	// client-to-server messages are expected once the ids arrived; this read
	// loop exists purely to detect the close.
	if false {
		go func() {
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					cancel()
					return
				}
			}
		}()
	}

	// Pulled sequentially, not with BulkContainerAction's bounded parallelism:
	// concurrent pulls would interleave progress frames from different images
	// on the one connection, and a bulk pull is not time-critical the way a
	// bulk restart/stop is.
	results := make([]bulkPullResult, 0, len(refs))
	for i, ref := range refs {
		if ctx.Err() != nil {
			break
		}
		send(bulkPullFrame{Ref: ref, Index: i + 1, Count: len(refs), Started: true})
		pullErr := s.docker.PullImage(ctx, hostID, ref, func(p docker.PullProgress) {
			send(bulkPullFrame{Ref: ref, Index: i + 1, Count: len(refs), Progress: &p})
		})
		if pullErr != nil && ctx.Err() != nil {
			// Aborted because the client is gone, not a real daemon failure.
			// Still worth its own audit entry, separate from a genuine
			// failure: layers may already have downloaded and a registry
			// credential may already have been used before the cancel
			// landed, and incident response needs to know who started and
			// cancelled this, not just see the operation vanish.
			s.audit(r, "image.pull", ref, "cancelled: client disconnected")
			break
		}
		res := bulkPullResult{Ref: ref, ContainerIDs: containersByRef[ref]}
		if pullErr != nil {
			res.Error = pullErr.Error()
			s.audit(r, "image.pull", ref, pullErr.Error())
			send(bulkPullFrame{Ref: ref, Index: i + 1, Count: len(refs), RefDone: true, Error: pullErr.Error()})
		} else {
			res.OK = true
			s.audit(r, "image.pull", ref, "")
			send(bulkPullFrame{Ref: ref, Index: i + 1, Count: len(refs), RefDone: true, OK: true})
		}
		results = append(results, res)
	}
	if ctx.Err() == nil {
		send(bulkPullFrame{AllDone: true, Results: results})
	}
}

func (s *Server) handleListNetworks(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	nets, err := s.docker.ListNetworks(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nets)
}

func (s *Server) handleRemoveNetwork(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.docker.RemoveNetwork(r.Context(), hostID, id); err != nil {
		// The daemon rejects predefined or in-use networks; surface that text
		// so the UI can explain why instead of failing opaquely.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "network.remove", id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCreateNetwork(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	var req docker.NetworkCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	id, err := s.docker.CreateNetwork(r.Context(), hostID, req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "network.create", req.Name, req.Driver)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleConnectNetwork(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		Container string `json:"container"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Container == "" {
		writeErr(w, http.StatusBadRequest, "container is required")
		return
	}
	if err := s.docker.ConnectNetwork(r.Context(), hostID, id, body.Container); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "network.connect", id, body.Container)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDisconnectNetwork(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		Container string `json:"container"`
		Force     bool   `json:"force"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Container == "" {
		writeErr(w, http.StatusBadRequest, "container is required")
		return
	}
	if err := s.docker.DisconnectNetwork(r.Context(), hostID, id, body.Container, body.Force); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "network.disconnect", id, body.Container)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePruneNetworks(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	deleted, err := s.docker.PruneNetworks(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "network.prune", "", "")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	top, err := s.docker.Topology(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, top)
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	info, err := s.docker.SystemInfo(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleProbePorts actively fingerprints a container's published ports.
func (s *Server) handleProbePorts(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	probes, err := s.docker.ProbeContainerPorts(ctx, hostID, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "container.probe", id, "")
	writeJSON(w, http.StatusOK, probes)
}

// handleHostPorts scans every published port of every running container on the
// host and fingerprints what's listening (the host-wide "open ports" map).
func (s *Server) handleHostPorts(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	ports, err := s.docker.ProbeHostPorts(ctx, hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "host.ports.scan", "", "")
	writeJSON(w, http.StatusOK, ports)
}

// handleStatsOverview reports how running containers divide up the host's CPU
// and memory — the data behind the dashboard's usage breakdown.
func (s *Server) handleStatsOverview(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	info, err := s.docker.SystemInfo(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	hid, _ := s.docker.ResolveHostID(r.Context(), hostID)

	// Serve per-container stats from the monitor's background snapshot rather
	// than re-sampling on demand. A single ContainerStats call costs ~1s (the
	// daemon's collection interval); with many containers, sampling them all on
	// every request hammered the daemon and slowed every other call.
	out := docker.ResourceOverview{CPUs: info.CPUs, MemTotal: info.MemTotal, Containers: []docker.ResourceUsage{}}
	for _, c := range s.monitor.Snapshot() {
		if c.HostID != hid || c.State != "running" {
			continue
		}
		cpuShare := c.CPUPercent
		if info.CPUs > 0 {
			cpuShare = c.CPUPercent / float64(info.CPUs)
		}
		var memShare float64
		if info.MemTotal > 0 {
			memShare = float64(c.MemBytes) / float64(info.MemTotal) * 100
		}
		out.Containers = append(out.Containers, docker.ResourceUsage{
			ID: c.ID, Name: c.Name, CPUPercent: cpuShare, MemBytes: c.MemBytes, MemPercent: memShare,
			NetRxRate: c.NetRxRate, NetTxRate: c.NetTxRate,
		})
	}
	sort.SliceStable(out.Containers, func(i, j int) bool {
		a, b := out.Containers[i], out.Containers[j]
		if a.CPUPercent != b.CPUPercent {
			return a.CPUPercent > b.CPUPercent
		}
		return a.MemBytes > b.MemBytes
	})
	writeJSON(w, http.StatusOK, out)
}

// handleInspect returns the daemon's raw JSON for an object. The object id/ref
// is a query param (refs contain ':' and '/', which path segments mangle).
func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	kind := chi.URLParam(r, "kind")
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	raw, err := s.docker.InspectRaw(r.Context(), hostID, kind, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// handleExportContainer streams a container's filesystem as a downloadable tar.
func (s *Server) handleExportContainer(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	rc, err := s.docker.ExportContainer(r.Context(), hostID, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	defer rc.Close()
	s.audit(r, "container.export", id, "")
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id[:min(12, len(id))]+`.tar"`)
	_, _ = io.Copy(w, rc)
}

func (s *Server) handleContainerDiff(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	diff, err := s.docker.ContainerDiff(r.Context(), hostID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) handleContainerTop(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	top, err := s.docker.ContainerTop(r.Context(), hostID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, top)
}

func (s *Server) handleImageHistory(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	hist, err := s.docker.ImageHistory(r.Context(), hostID, ref)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hist)
}

func (s *Server) handleDiskUsage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	du, err := s.docker.DiskUsage(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, du)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	entries, err := s.store.RecentAudit(r.Context(), limit, before)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read audit log")
		return
	}
	// An entry names the host its action touched, so an unfiltered log would
	// describe activity on hosts the reader was scoped away from — the same leak
	// as the alert feed, in a section that is itself authority-level. Entries with
	// no host dimension (host 0) stay visible to everyone who holds "audit".
	visible := s.visibleHosts(r)
	shown := make([]store.AuditEntry, 0, len(entries))
	for _, e := range entries {
		if visible(e.HostID) {
			shown = append(shown, e)
		}
	}
	writeJSON(w, http.StatusOK, shown)
}

// handleNetworkStats reports endpoint traffic for one network.
//
// Docker has no per-network counters, so this aggregates the attached
// containers' own totals — and only those attached to exactly ONE network, whose
// traffic is therefore unambiguously this network's. Multiply-attached
// containers are listed but not summed; see docker.NetworkEndpointTraffic.
func (s *Server) handleNetworkStats(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")

	// Container attachments are recorded by network NAME, so resolve it.
	nets, err := s.docker.ListNetworks(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	name := ""
	for _, n := range nets {
		if n.ID == id || n.Name == id {
			name = n.Name
			break
		}
	}
	if name == "" {
		writeErr(w, http.StatusNotFound, "network not found")
		return
	}

	// Counters come from the monitor's latest snapshot rather than a fresh stats
	// call per container: a network with twenty containers would otherwise mean
	// twenty round trips to the daemon on every poll.
	var lookup func(string) (uint64, uint64, uint64, uint64, uint64, uint64, bool)
	if s.monitor != nil {
		snap := map[string]monitor.ContainerStat{}
		for _, cs := range s.monitor.Snapshot() {
			snap[cs.ID] = cs
		}
		lookup = func(cid string) (uint64, uint64, uint64, uint64, uint64, uint64, bool) {
			cs, ok := snap[cid]
			if !ok {
				return 0, 0, 0, 0, 0, 0, false
			}
			// The monitor keeps byte totals only; drops and errors are not in the
			// cached snapshot, so they are reported as zero here rather than
			// triggering a per-container stats call.
			return cs.NetRx, cs.NetTx, 0, 0, 0, 0, true
		}
	}

	out, err := s.docker.NetworkEndpointTraffic(r.Context(), hostID, name, lookup)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	out.NetworkID = id
	writeJSON(w, http.StatusOK, out)
}
