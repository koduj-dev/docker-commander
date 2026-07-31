package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/store"
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
		fmt.Fprintf(&b, "dockercmd_container_running{id=%q,name=%q,host=%q}  %d\n", short(c.ID), c.Name, c.HostName, running)
	}

	// The help text used to say "host-relative", which it is not: this is the
	// `docker stats` figure where 100% is ONE core, so a container busy on four
	// reads ~400%. Anyone building a dashboard on the old description would have
	// drawn the wrong conclusion on every multi-core host.
	writeMetricHeader(&b, "dockercmd_container_cpu_percent", "gauge", "Container CPU usage percent, docker-stats convention (100 = one core; divide by dockercmd_container_cpu_cores for a share of the machine)")
	forRunning(s.monitor, func(c monitor.ContainerStat) {
		fmt.Fprintf(&b, "dockercmd_container_cpu_percent{id=%q,name=%q,host=%q}  %g\n", short(c.ID), c.Name, c.HostName, c.CPUPercent)
	})

	writeMetricHeader(&b, "dockercmd_container_cpu_cores", "gauge", "Cores the daemon reports for this container's host, so cpu_percent can be normalised")
	forRunning(s.monitor, func(c monitor.ContainerStat) {
		fmt.Fprintf(&b, "dockercmd_container_cpu_cores{id=%q,name=%q,host=%q}  %g\n", short(c.ID), c.Name, c.HostName, c.CPUCores)
	})

	writeMetricHeader(&b, "dockercmd_container_mem_bytes", "gauge", "Container memory usage in bytes")
	forRunning(s.monitor, func(c monitor.ContainerStat) {
		fmt.Fprintf(&b, "dockercmd_container_mem_bytes{id=%q,name=%q,host=%q}  %d\n", short(c.ID), c.Name, c.HostName, c.MemBytes)
	})

	writeMetricHeader(&b, "dockercmd_container_mem_percent", "gauge", "Container memory usage percent of limit")
	forRunning(s.monitor, func(c monitor.ContainerStat) {
		fmt.Fprintf(&b, "dockercmd_container_mem_percent{id=%q,name=%q,host=%q}  %g\n", short(c.ID), c.Name, c.HostName, c.MemPercent)
	})

	// Alerting state. Now that a threshold alert is a condition with a lifetime
	// rather than a repeated line, "what is wrong right now" is a real number the
	// engine already holds — and it is the thing you would actually page on.
	s.writeAlertMetrics(r, &b)

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

// writeAlertMetrics exposes live alert conditions and the outstanding count.
//
// Deliberately NOT scoped to a user: /metrics is a machine endpoint guarded by
// its own token, and a scrape has no session. That is the existing contract for
// this endpoint — container stats are already unscoped — but it is worth stating,
// because everything else in the app filters by host.
func (s *Server) writeAlertMetrics(r *http.Request, b *strings.Builder) {
	ctx := r.Context()

	states, err := s.store.ListAlertStates(ctx)
	if err == nil {
		writeMetricHeader(b, "dockercmd_alert_firing", "gauge",
			"1 for each condition currently over its threshold, labelled by what it is about")
		for _, st := range states {
			fmt.Fprintf(b, "dockercmd_alert_firing{host=%q,container=%q,metric=%q,severity=%q,rule=%q}  1\n",
				st.HostName, st.ContainerName, st.Metric, st.Severity, st.RuleName)
		}
		writeMetricHeader(b, "dockercmd_alerts_firing_count", "gauge", "Number of conditions currently firing")
		fmt.Fprintf(b, "dockercmd_alerts_firing_count  %d\n", len(states))
	}

	// Matches the sidebar badge: unacknowledged warnings and criticals. A
	// resolution is stored already settled, so this never climbs because
	// something got better.
	if _, n, err := s.store.ListAlertEvents(ctx, store.AlertQuery{
		Unacked: true, Severities: []string{"warning", "critical"}, Limit: 1,
	}); err == nil {
		writeMetricHeader(b, "dockercmd_alerts_outstanding", "gauge",
			"Unacknowledged warning and critical alerts")
		fmt.Fprintf(b, "dockercmd_alerts_outstanding  %d\n", n)
	}
}
