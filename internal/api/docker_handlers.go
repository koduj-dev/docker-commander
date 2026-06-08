package api

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list hosts")
		return
	}
	// Shape a safe view; never leak TLS key material to the client.
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, map[string]any{
			"id": h.ID, "name": h.Name, "kind": h.Kind, "address": h.Address,
			"alertEmail": h.AlertEmail, "disabled": h.Disabled,
		})
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
	writeJSON(w, http.StatusOK, entries)
}
