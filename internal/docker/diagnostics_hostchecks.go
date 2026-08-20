package docker

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/network"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// diskWarnFreePct and diskFailFreePct are fixed v1 thresholds — not yet
// configurable, unlike the alert system's per-rule thresholds.
const (
	diskWarnFreePct = 15.0
	diskFailFreePct = 5.0
)

// resolveHost turns a possibly-zero hostID into the store row it actually
// names, using the same "0/negative means whichever host is local, or the
// first host" resolution as probeTarget/Client — so a diagnostics probe looks
// at the SAME host every other call for this hostID would talk to, rather
// than assuming 0 always means "local" (it means "unspecified"; the resolved
// default host could itself be remote).
func (m *Manager) resolveHost(ctx context.Context, hostID int64) (*store.Host, error) {
	if hostID <= 0 {
		id, err := m.defaultHostID(ctx)
		if err != nil {
			return nil, err
		}
		hostID = id
	}
	return m.store.HostByID(ctx, hostID)
}

// hostProbeFor returns the host's network interfaces, probed locally or over
// SSH depending on its kind. A "tcp"-kind host has no shell to probe at all —
// reported the same as every probe attempt failing, rather than as a special
// case each check has to know about separately.
func (m *Manager) hostProbeFor(ctx context.Context, hostID int64) hostProbeResult {
	h, err := m.resolveHost(ctx, hostID)
	if err != nil {
		return hostProbeResult{Attempted: []string{"resolve host"}, Err: err}
	}
	switch h.Kind {
	case "local", "":
		return localHostProbe()
	case "ssh":
		return m.remoteHostProbe(ctx, h.ID, h)
	default: // "tcp" and anything else: no shell path to run a probe over
		return hostProbeResult{Attempted: []string{fmt.Sprintf("host kind %q has no remote shell", h.Kind)}}
	}
}

// nonDockerIfaces filters out loopback and Docker-owned interfaces — a bridge
// trivially "overlaps" the network it's the gateway for, which is not the bug
// this check exists to catch.
func nonDockerIfaces(ifaces []hostIface) []hostIface {
	var out []hostIface
	for _, hi := range ifaces {
		if dockerOwnedIface(hi.Name) {
			continue
		}
		out = append(out, hi)
	}
	return out
}

// checkNetworkHostOverlap flags a Docker network whose subnet collides with
// one of the host's own real network interfaces — a docker0-shaped bridge
// racing a corporate LAN or VPN range for the same addresses, which is what
// prompted this whole battery: it looks like nothing is wrong until routing
// silently breaks.
func checkNetworkHostOverlap(nets []NetworkSummary, probe hostProbeResult) CheckResult {
	const id, name = "network_host_overlap", "Docker network vs. host network overlap"
	if probe.Err != nil || probe.Method == "" {
		return CheckResult{
			ID: id, Name: name, Status: CheckSkipped,
			Message: "Could not determine the host's real network interfaces (tried: " +
				strings.Join(probe.Attempted, ", ") + ")",
		}
	}
	hostIfaces := nonDockerIfaces(probe.Ifaces)

	var details []string
	for _, n := range nets {
		for _, s := range n.Subnets {
			_, dockerNet, err := net.ParseCIDR(s)
			if err != nil {
				continue
			}
			for _, hi := range hostIfaces {
				for _, hs := range hi.Subnets {
					_, hostNet, err := net.ParseCIDR(hs)
					if err != nil {
						continue
					}
					if dockerNet.Contains(hostNet.IP) || hostNet.Contains(dockerNet.IP) {
						details = append(details, fmt.Sprintf("Docker network %q (%s) overlaps host interface %q (%s)",
							n.Name, dockerNet, hi.Name, hostNet))
					}
				}
			}
		}
	}
	sort.Strings(details)
	if len(details) > 0 {
		return CheckResult{
			ID: id, Name: name, Status: CheckFail,
			Message: fmt.Sprintf("%d Docker network(s) overlap a real host interface", len(details)),
			Details: details,
		}
	}
	return CheckResult{ID: id, Name: name, Status: CheckOK, Message: "No Docker network overlaps a host interface"}
}

// checkMTUMismatch flags a bridge network whose configured MTU differs from
// the host's default-route interface — a common source of silent packet
// fragmentation/drops that never surfaces as a clean error.
func (m *Manager) checkMTUMismatch(ctx context.Context, hostID int64, nets []NetworkSummary, probe hostProbeResult) CheckResult {
	const id, name = "mtu_mismatch", "MTU mismatch"
	var defaultIface *hostIface
	for i := range probe.Ifaces {
		if probe.Ifaces[i].IsDefault {
			defaultIface = &probe.Ifaces[i]
			break
		}
	}
	if probe.Err != nil || probe.Method == "" || defaultIface == nil || defaultIface.MTU == 0 {
		return CheckResult{
			ID: id, Name: name, Status: CheckSkipped,
			Message: "Could not determine the host's default-route interface MTU",
		}
	}

	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return CheckResult{ID: id, Name: name, Status: CheckSkipped, Message: "Could not reach the Docker daemon to inspect network options"}
	}

	var details []string
	for _, n := range nets {
		full, err := cli.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if err != nil || full.Driver != "bridge" {
			continue
		}
		raw, ok := full.Options["com.docker.network.driver.mtu"]
		if !ok {
			continue
		}
		var netMTU int
		if _, err := fmt.Sscanf(raw, "%d", &netMTU); err != nil || netMTU == 0 {
			continue
		}
		if netMTU != defaultIface.MTU {
			details = append(details, fmt.Sprintf("network %q: MTU %d, host interface %q: MTU %d",
				n.Name, netMTU, defaultIface.Name, defaultIface.MTU))
		}
	}
	sort.Strings(details)
	if len(details) > 0 {
		return CheckResult{
			ID: id, Name: name, Status: CheckWarn,
			Message: fmt.Sprintf("%d Docker network(s) have an MTU that does not match the host's default interface", len(details)),
			Details: details,
		}
	}
	return CheckResult{ID: id, Name: name, Status: CheckOK, Message: "Every bridge network's MTU matches the host's default interface"}
}

// checkDiskSpace flags a host running low on free space where Docker actually
// stores its data (SystemInfo.DockerRootDir), not just "/" — a full disk is a
// classic cause of containers failing in ways that look unrelated.
func (m *Manager) checkDiskSpace(ctx context.Context, hostID int64, info *SystemInfo) CheckResult {
	const id, name = "disk_space", "Host disk space"
	h, err := m.resolveHost(ctx, hostID)
	if err != nil {
		return CheckResult{ID: id, Name: name, Status: CheckSkipped, Message: "Could not determine free disk space: " + errString(err)}
	}

	var total, free uint64
	switch h.Kind {
	case "local", "":
		total, free, err = diskFree(info.DockerRootDir)
	case "ssh":
		total, free, err = m.remoteDiskFree(ctx, h.ID, h, info.DockerRootDir)
	default:
		return CheckResult{
			ID: id, Name: name, Status: CheckSkipped,
			Message: fmt.Sprintf("host kind %q has no remote shell to check free space with", h.Kind),
		}
	}
	if err != nil || total == 0 {
		return CheckResult{ID: id, Name: name, Status: CheckSkipped, Message: "Could not determine free disk space: " + errString(err)}
	}

	freePct := float64(free) / float64(total) * 100
	msg := fmt.Sprintf("%.1f%% free at %s", freePct, info.DockerRootDir)
	switch {
	case freePct < diskFailFreePct:
		return CheckResult{ID: id, Name: name, Status: CheckFail, Message: msg}
	case freePct < diskWarnFreePct:
		return CheckResult{ID: id, Name: name, Status: CheckWarn, Message: msg}
	default:
		return CheckResult{ID: id, Name: name, Status: CheckOK, Message: msg}
	}
}

func errString(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
