package docker

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
)

// logDriverCheckMaxContainers bounds how many running containers
// checkLogDriverRotation will inspect, so a host with an unusually large
// container count can't turn one diagnostics run into hundreds of inspect
// calls.
const logDriverCheckMaxContainers = 500

// Diagnostics is a battery of read-only sanity checks against a host: the
// kind of silent misconfiguration (an overlapping subnet, a port two
// containers both think they own, logs quietly filling the disk) that costs
// hours to track down by hand because nothing about it shows up as an error
// until something else breaks.

// CheckStatus is the outcome of one diagnostic check.
type CheckStatus string

const (
	CheckOK      CheckStatus = "ok"
	CheckWarn    CheckStatus = "warn"
	CheckFail    CheckStatus = "fail"
	CheckSkipped CheckStatus = "skipped"
)

// CheckResult is one row of a diagnostics report.
type CheckResult struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message"`
	Details []string    `json:"details,omitempty"`
}

// DiagnosticsReport is the full battery's result for one host.
type DiagnosticsReport struct {
	HostID      int64         `json:"hostId"`
	GeneratedAt time.Time     `json:"generatedAt"`
	Checks      []CheckResult `json:"checks"`
}

// predefinedNetworks are the networks every Docker daemon creates itself —
// excluded from the dangling-resource check (they're never "unused cruft").
var predefinedNetworks = map[string]bool{"bridge": true, "host": true, "none": true}

// RunDiagnostics runs the full check battery against one host. It fetches
// each shared input once and hands already-fetched data to the (pure)
// individual checks, in a fixed order that the UI renders as-is.
func (m *Manager) RunDiagnostics(ctx context.Context, hostID int64) (*DiagnosticsReport, error) {
	nets, err := m.ListNetworks(ctx, hostID)
	if err != nil {
		return nil, err
	}
	containers, err := m.ListContainers(ctx, hostID)
	if err != nil {
		return nil, err
	}
	vols, err := m.ListVolumes(ctx, hostID)
	if err != nil {
		return nil, err
	}
	info, err := m.SystemInfo(ctx, hostID)
	if err != nil {
		return nil, err
	}

	// Computed once and shared by the two checks that need it: probing a
	// remote host's interfaces is the most expensive step in the battery, and
	// both checks report the same "skipped" reason when it fails.
	probe := m.hostProbeFor(ctx, hostID)

	report := &DiagnosticsReport{HostID: hostID, GeneratedAt: time.Now()}
	report.Checks = append(report.Checks, checkNetworkOverlap(nets))
	report.Checks = append(report.Checks, checkNetworkHostOverlap(nets, probe))
	report.Checks = append(report.Checks, m.checkMTUMismatch(ctx, hostID, nets, probe))
	report.Checks = append(report.Checks, checkDuplicatePortBindings(containers))
	logDriver, err := m.checkLogDriverRotation(ctx, hostID, containers, info)
	if err != nil {
		return nil, err
	}
	report.Checks = append(report.Checks, logDriver)
	report.Checks = append(report.Checks, m.checkDiskSpace(ctx, hostID, info))
	report.Checks = append(report.Checks, checkDanglingResources(nets, vols))
	return report, nil
}

// checkLogDriverRotation fetches each running container's effective log
// driver and hands it to the pure evaluateLogDrivers. json-file (Docker's
// default when nothing is configured) with no max-size keeps growing
// forever — a common way for a quiet container to fill the host's disk
// without ever looking broken until it does.
func (m *Manager) checkLogDriverRotation(ctx context.Context, hostID int64, containers []ContainerSummary, info *SystemInfo) (CheckResult, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return CheckResult{}, err
	}
	var running []ContainerSummary
	for _, c := range containers {
		if c.State == "running" {
			running = append(running, c)
		}
	}
	truncated := false
	if len(running) > logDriverCheckMaxContainers {
		running = running[:logDriverCheckMaxContainers]
		truncated = true
	}
	logConfigs := make(map[string]container.LogConfig, len(running))
	for _, c := range running {
		inspect, err := cli.ContainerInspect(ctx, c.ID)
		if err != nil || inspect.HostConfig == nil {
			continue // a container that vanished mid-scan just isn't counted
		}
		logConfigs[c.Name] = inspect.HostConfig.LogConfig
	}
	result := evaluateLogDrivers(logConfigs, info.LoggingDriver)
	if truncated {
		result.Message += fmt.Sprintf(" (checked the first %d running containers)", logDriverCheckMaxContainers)
	}
	return result, nil
}

// evaluateLogDrivers is the pure half of checkLogDriverRotation: given each
// container's effective LogConfig (already resolved against the daemon
// default), which ones are json-file/local without a max-size cap.
//
// A container's HostConfig.LogConfig.Config is the RESOLVED configuration,
// not "only what this container explicitly overrode": Docker bakes any
// daemon.json `log-opts` default into it at creation time, so a container
// created under a daemon with a global `max-size` already carries that value
// in its own Config map — confirmed empirically against a daemon with
// `log-opts.max-size` set in daemon.json (docker:29-dind), where
// `docker inspect` on a container created with no per-container --log-opt at
// all showed `Config: {"max-size": "10m", ...}`. An absent max-size here is
// therefore genuinely unbounded, not just "no per-container override to
// report" — see TestEvaluateLogDrivers's daemon-default case.
func evaluateLogDrivers(logConfigs map[string]container.LogConfig, daemonDefault string) CheckResult {
	var details []string
	for name, cfg := range logConfigs {
		driver := cfg.Type
		if driver == "" {
			driver = daemonDefault
		}
		if driver != "" && driver != "json-file" {
			continue // other drivers (journald, syslog, …) manage their own rotation
		}
		if cfg.Config["max-size"] == "" {
			details = append(details, name)
		}
	}
	sort.Strings(details)
	if len(details) > 0 {
		return CheckResult{
			ID: "log_driver_rotation", Name: "Log rotation", Status: CheckWarn,
			Message: fmt.Sprintf("%d container(s) logging with json-file and no max-size limit", len(details)),
			Details: details,
		}
	}
	return CheckResult{
		ID: "log_driver_rotation", Name: "Log rotation", Status: CheckOK,
		Message: "Every running container's logs are bounded or use a non-json-file driver",
	}
}

// checkNetworkOverlap flags Docker networks whose subnets overlap each
// other — two networks racing for the same address range means containers on
// one can silently end up unreachable, or routed onto the other.
func checkNetworkOverlap(nets []NetworkSummary) CheckResult {
	type sub struct {
		netName string
		cidr    *net.IPNet
	}
	var subs []sub
	for _, n := range nets {
		for _, s := range n.Subnets {
			_, ipnet, err := net.ParseCIDR(s)
			if err != nil {
				continue
			}
			subs = append(subs, sub{netName: n.Name, cidr: ipnet})
		}
	}
	var details []string
	for i := 0; i < len(subs); i++ {
		for j := i + 1; j < len(subs); j++ {
			if subs[i].netName == subs[j].netName {
				continue
			}
			// Two CIDR blocks either nest or are disjoint, so checking that
			// each network address falls inside the other block is a
			// sufficient overlap test.
			if subs[i].cidr.Contains(subs[j].cidr.IP) || subs[j].cidr.Contains(subs[i].cidr.IP) {
				details = append(details, fmt.Sprintf("%q (%s) overlaps %q (%s)",
					subs[i].netName, subs[i].cidr, subs[j].netName, subs[j].cidr))
			}
		}
	}
	sort.Strings(details)
	if len(details) > 0 {
		return CheckResult{
			ID: "network_overlap", Name: "Docker network subnet overlap", Status: CheckFail,
			Message: fmt.Sprintf("%d overlapping subnet pair(s) between Docker networks", len(details)),
			Details: details,
		}
	}
	return CheckResult{
		ID: "network_overlap", Name: "Docker network subnet overlap", Status: CheckOK,
		Message: "No overlapping subnets between Docker networks",
	}
}

// checkDuplicatePortBindings flags host ports published by more than one
// running container. Two containers binding the exact same host IP+port+proto
// is a real conflict (one of them silently isn't reachable the way it looks
// like it should be); the same port on different host IPs is a lesser
// "shadow" risk worth surfacing but not failing on.
func checkDuplicatePortBindings(containers []ContainerSummary) CheckResult {
	type binding struct {
		container, ip string
	}
	type portKey struct {
		port  uint16
		proto string
	}
	groups := map[portKey][]binding{}
	var keys []portKey
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		for _, p := range c.Ports {
			if p.PublicPort == 0 {
				continue
			}
			k := portKey{p.PublicPort, p.Type}
			if _, seen := groups[k]; !seen {
				keys = append(keys, k)
			}
			ip := p.IP
			if ip == "" {
				ip = "0.0.0.0"
			}
			groups[k] = append(groups[k], binding{c.Name, ip})
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].port != keys[j].port {
			return keys[i].port < keys[j].port
		}
		return keys[i].proto < keys[j].proto
	})

	var conflicts, shadows []string
	for _, k := range keys {
		bindings := groups[k]
		if len(bindings) < 2 {
			continue
		}
		byIP := map[string][]string{}
		var ips []string
		for _, b := range bindings {
			if _, seen := byIP[b.ip]; !seen {
				ips = append(ips, b.ip)
			}
			byIP[b.ip] = append(byIP[b.ip], b.container)
		}
		sort.Strings(ips)
		for _, ip := range ips {
			names := byIP[ip]
			if len(names) > 1 {
				sort.Strings(names)
				conflicts = append(conflicts, fmt.Sprintf("port %d/%s on %s: %s",
					k.port, k.proto, ip, strings.Join(names, ", ")))
			}
		}
		if len(ips) > 1 {
			var parts []string
			for _, ip := range ips {
				parts = append(parts, fmt.Sprintf("%s@%s", strings.Join(byIP[ip], "/"), ip))
			}
			shadows = append(shadows, fmt.Sprintf("port %d/%s published on multiple host IPs: %s",
				k.port, k.proto, strings.Join(parts, ", ")))
		}
	}

	switch {
	case len(conflicts) > 0:
		return CheckResult{
			ID: "duplicate_port_bindings", Name: "Duplicate port bindings", Status: CheckFail,
			Message: fmt.Sprintf("%d host port(s) bound by more than one running container", len(conflicts)),
			Details: append(conflicts, shadows...),
		}
	case len(shadows) > 0:
		return CheckResult{
			ID: "duplicate_port_bindings", Name: "Duplicate port bindings", Status: CheckWarn,
			Message: fmt.Sprintf("%d host port(s) published on more than one host IP", len(shadows)),
			Details: shadows,
		}
	}
	return CheckResult{
		ID: "duplicate_port_bindings", Name: "Duplicate port bindings", Status: CheckOK,
		Message: "No conflicting port bindings among running containers",
	}
}

// checkDanglingResources flags user-created networks and volumes that
// nothing is attached to — usually leftovers from a `compose down` without
// `-v`/prune, not a bug, but a frequent source of "why does this network
// already exist" confusion.
func checkDanglingResources(nets []NetworkSummary, vols []VolumeSummary) CheckResult {
	var details []string
	for _, n := range nets {
		if predefinedNetworks[n.Name] {
			continue
		}
		if len(n.Containers) == 0 {
			details = append(details, "network: "+n.Name)
		}
	}
	for _, v := range vols {
		if len(v.InUseBy) == 0 {
			details = append(details, "volume: "+v.Name)
		}
	}
	sort.Strings(details)
	if len(details) > 0 {
		return CheckResult{
			ID: "dangling_resources", Name: "Dangling networks & volumes", Status: CheckWarn,
			Message: fmt.Sprintf("%d unused network(s)/volume(s) found", len(details)),
			Details: details,
		}
	}
	return CheckResult{
		ID: "dangling_resources", Name: "Dangling networks & volumes", Status: CheckOK,
		Message: "No unused networks or volumes",
	}
}
