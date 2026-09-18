package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
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

// topTalkerWindows are the only windows offered — a fixed allow-list rather
// than a raw duration string, since the window becomes a history query's
// `since` and an unbounded one could ask for far more than the UI needs.
var topTalkerWindows = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
}

type topTalker struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	HostID   int64   `json:"hostId"`
	HostName string  `json:"hostName"`
	RxRate   float64 `json:"rxRate"` // bytes/s, averaged over the window
	TxRate   float64 `json:"txRate"` // bytes/s, averaged over the window
	Rate     float64 `json:"rate"`   // the requested metric's rate — what rows are sorted by
}

// topTalkerMeta is the display info the handler already has from the live
// snapshot (name/host), keyed by container id.
type topTalkerMeta struct {
	name, hostName string
	hostID         int64
}

// rankTopTalkers is handleTopTalkers' testable core: given a history store,
// the running containers' ids/meta, and a window, it ranks by the requested
// metric's rate averaged over that window. Split out from the handler so the
// ranking/skip logic can be tested directly against a real in-memory
// history.Store, without a live Monitor/Docker daemon behind it.
func rankTopTalkers(ctx context.Context, hist history.Store, ids []string, metaByID map[string]topTalkerMeta, since time.Time, metric string, limit int) ([]topTalker, error) {
	rx, err := hist.QueryAll(ctx, history.MetricNetRx, since, ids)
	if err != nil {
		return nil, err
	}
	tx, err := hist.QueryAll(ctx, history.MetricNetTx, since, ids)
	if err != nil {
		return nil, err
	}

	out := make([]topTalker, 0, len(ids))
	for _, cid := range ids {
		rxRate, rxOK := history.RateOverWindow(rx[cid])
		txRate, txOK := history.RateOverWindow(tx[cid])
		if !rxOK && !txOK {
			continue // not enough history yet (e.g. a container that just started)
		}
		meta := metaByID[cid]
		t := topTalker{ID: cid, Name: meta.name, HostID: meta.hostID, HostName: meta.hostName, RxRate: rxRate, TxRate: txRate}
		switch metric {
		case history.MetricNetRx:
			t.Rate = rxRate
		case history.MetricNetTx:
			t.Rate = txRate
		default:
			t.Rate = rxRate + txRate
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rate > out[j].Rate })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// handleTopTalkers ranks running containers by network throughput AVERAGED
// OVER A STORED WINDOW — never a point-in-time poll sample, which reorders
// itself every poll (8-15s) and is unreadable; see the "top talkers" note in
// docs/alerts.md and ResourceBreakdown.tsx's comment on why the live
// dashboard snapshot deliberately doesn't attempt this ranking.
// Query params: window ("5m"|"15m"|"1h", default "5m"), metric
// ("total"|"netrx"|"nettx", default "total"), limit (1-50, default 8).
func (s *Server) handleTopTalkers(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeJSON(w, http.StatusOK, map[string]any{"window": "", "metric": "", "containers": []topTalker{}})
		return
	}
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	windowName := r.URL.Query().Get("window")
	if windowName == "" {
		windowName = "5m"
	}
	win, ok := topTalkerWindows[windowName]
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown window")
		return
	}
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "total"
	}
	if metric != "total" && metric != history.MetricNetRx && metric != history.MetricNetTx {
		writeErr(w, http.StatusBadRequest, "unknown metric")
		return
	}
	limit := 8
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 1
	} else if limit > 50 {
		limit = 50
	}

	hid, _ := s.docker.ResolveHostID(r.Context(), hostID)
	metaByID := make(map[string]topTalkerMeta)
	ids := make([]string, 0)
	for _, cs := range s.monitor.Snapshot() {
		if cs.HostID != hid || cs.State != "running" {
			continue
		}
		ids = append(ids, cs.ID)
		metaByID[cs.ID] = topTalkerMeta{name: cs.Name, hostName: cs.HostName, hostID: cs.HostID}
	}

	out, err := rankTopTalkers(r.Context(), s.history, ids, metaByID, time.Now().Add(-win), metric, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "history query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"window": windowName, "metric": metric, "containers": out})
}
