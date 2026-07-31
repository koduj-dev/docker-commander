package api

import (
	"net/http"
	"time"

	"github.com/koduj-dev/docker-commander/internal/history"
)

// historyMetrics is the set of metric names the endpoint will serve.
//
// A package-level var rather than a switch inside the handler so a test can
// compare it against what the engine actually records — the two drifting apart
// is the failure mode here, and it is silent: an unlisted metric returns 400 the
// UI shows as an empty graph, and a listed-but-unrecorded one returns an empty
// series that reads as "no traffic".
var historyMetrics = map[string]bool{
	history.MetricCPU:       true,
	history.MetricMem:       true,
	history.MetricMemBytes:  true,
	history.MetricNetRx:     true,
	history.MetricNetTx:     true,
	history.MetricNetDrops:  true,
	history.MetricNetErrors: true,
}

// handleMetricsHistory returns a time series for one container+metric.
// Query params: container (id), metric, range (e.g. 30m, 6h).
//
// Metric is whitelisted rather than passed through: it becomes part of the
// storage key in both backends, so an unrecognised value would silently query a
// series that does not exist and return an empty graph that looks like "no
// traffic" instead of "wrong request". Network metrics are cumulative counters —
// callers derive rates from consecutive points.
func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	if containerID == "" {
		writeErr(w, http.StatusBadRequest, "container is required")
		return
	}
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = history.MetricCPU
	}
	if !historyMetrics[metric] {
		writeErr(w, http.StatusBadRequest, "unknown metric")
		return
	}

	// The series is keyed by container id alone, so knowing an id would otherwise
	// be enough to read a container's CPU/memory history from a host the caller
	// was scoped away from. Authorise against the host the samples were recorded
	// from. A container with no recorded host is unknown, not local: allow it only
	// for callers who can reach every host anyway, so a scoped caller can't probe
	// ids to find out what exists.
	if s.history != nil {
		hostID, known, err := s.history.HostFor(r.Context(), containerID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not determine the container's host")
			return
		}
		if !known {
			hostID = -1 // reachable only by a caller with no host restriction
		}
		if !s.callerCanReachHost(r, hostID) {
			writeErr(w, http.StatusForbidden, "your access does not include that container's host")
			return
		}
	}

	rng := 30 * time.Minute
	if v := r.URL.Query().Get("range"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			rng = d
		}
	}

	points, err := s.history.Query(r.Context(), containerID, metric, time.Now().Add(-rng))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "history query failed")
		return
	}
	if points == nil {
		points = []history.Point{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"metric": metric, "points": points})
}
