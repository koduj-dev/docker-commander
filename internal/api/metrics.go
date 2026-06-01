package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/monitor"
)

// handleMetrics serves a Prometheus text exposition of the latest container
// stats snapshot, so Grafana (via Prometheus scrape) and similar tools can
// ingest the data. Optionally protected by a bearer token.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metricsToken != "" && !s.metricsAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.monitor == nil {
		http.Error(w, "monitor unavailable", http.StatusServiceUnavailable)
		return
	}

	var b strings.Builder
	writeMetricHeader(&b, "dockercmd_container_running", "gauge", "1 if the container is running, else 0")
	for _, c := range s.monitor.Snapshot() {
		running := 0
		if c.State == "running" {
			running = 1
		}
		fmt.Fprintf(&b, "dockercmd_container_running{id=%q,name=%q}  %d\n", short(c.ID), c.Name, running)
	}

	writeMetricHeader(&b, "dockercmd_container_cpu_percent", "gauge", "Container CPU usage percent (host-relative)")
	forRunning(s.monitor, func(c monitor.ContainerStat) {
		fmt.Fprintf(&b, "dockercmd_container_cpu_percent{id=%q,name=%q}  %g\n", short(c.ID), c.Name, c.CPUPercent)
	})

	writeMetricHeader(&b, "dockercmd_container_mem_bytes", "gauge", "Container memory usage in bytes")
	forRunning(s.monitor, func(c monitor.ContainerStat) {
		fmt.Fprintf(&b, "dockercmd_container_mem_bytes{id=%q,name=%q}  %d\n", short(c.ID), c.Name, c.MemBytes)
	})

	writeMetricHeader(&b, "dockercmd_container_mem_percent", "gauge", "Container memory usage percent of limit")
	forRunning(s.monitor, func(c monitor.ContainerStat) {
		fmt.Fprintf(&b, "dockercmd_container_mem_percent{id=%q,name=%q}  %g\n", short(c.ID), c.Name, c.MemPercent)
	})

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) metricsAuthorized(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); h == "Bearer "+s.metricsToken {
		return true
	}
	return r.URL.Query().Get("token") == s.metricsToken
}

func forRunning(m *monitor.Monitor, fn func(monitor.ContainerStat)) {
	for _, c := range m.Snapshot() {
		if c.State == "running" {
			fn(c)
		}
	}
}

func writeMetricHeader(b *strings.Builder, name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
