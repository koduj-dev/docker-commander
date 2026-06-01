package api

import (
	"net/http"
	"time"

	"github.com/koduj-dev/docker-commander/internal/history"
)

// handleMetricsHistory returns a time series for one container+metric.
// Query params: container (id), metric (cpu|mem|membytes), range (e.g. 30m, 6h).
func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	if containerID == "" {
		writeErr(w, http.StatusBadRequest, "container is required")
		return
	}
	metric := r.URL.Query().Get("metric")
	switch metric {
	case history.MetricCPU, history.MetricMem, history.MetricMemBytes:
	case "":
		metric = history.MetricCPU
	default:
		writeErr(w, http.StatusBadRequest, "unknown metric")
		return
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
