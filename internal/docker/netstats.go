package docker

import (
	"context"
	"sort"
)

// Endpoint traffic for a Docker network.
//
// Docker does not report per-network counters. `/containers/{id}/stats` is keyed
// by INTERFACE name (eth0, eth1…) and carries no network identity on Linux — the
// `endpoint_id` field exists in the API but the daemon only fills it on Windows.
// `docker stats` itself shows a single aggregate NET I/O column for exactly this
// reason.
//
// What IS knowable: a container attached to exactly ONE network has all of its
// traffic on that network. That covers the common case, so this attributes those
// containers with confidence and refuses to guess for the rest — a
// multiply-attached container is counted as unattributable rather than being
// split by assumption.

// NetworkEndpointStats is one container's contribution to a network.
type NetworkEndpointStats struct {
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
	RxBytes       uint64 `json:"rxBytes"`
	TxBytes       uint64 `json:"txBytes"`
	RxDropped     uint64 `json:"rxDropped"`
	TxDropped     uint64 `json:"txDropped"`
	RxErrors      uint64 `json:"rxErrors"`
	TxErrors      uint64 `json:"txErrors"`
	// Attributable is false when the container is on several networks, so its
	// counters cover all of them and cannot honestly be assigned to this one.
	Attributable bool `json:"attributable"`
}

// NetworkStats aggregates a network's attached containers.
type NetworkStats struct {
	NetworkID string `json:"networkId"`
	// RxBytes/TxBytes total ONLY the attributable endpoints. Labelled "endpoint"
	// deliberately: container-to-container traffic inside a network is counted
	// twice, once as each side's TX and once as its RX, so this is not the volume
	// of traffic on the wire and must never be presented as such.
	RxBytes      uint64 `json:"rxBytes"`
	TxBytes      uint64 `json:"txBytes"`
	RxDropped    uint64 `json:"rxDropped"`
	TxDropped    uint64 `json:"txDropped"`
	RxErrors     uint64 `json:"rxErrors"`
	TxErrors     uint64 `json:"txErrors"`
	Endpoints    int    `json:"endpoints"`
	Unattributed int    `json:"unattributed"`

	Containers []NetworkEndpointStats `json:"containers"`
}

// statsFor supplies a container's cumulative counters; nil means unknown.
type statsFor func(containerID string) (rx, tx, rxDrop, txDrop, rxErr, txErr uint64, ok bool)

// NetworkEndpointTraffic builds the endpoint view for one network.
func (m *Manager) NetworkEndpointTraffic(ctx context.Context, hostID int64, networkName string, lookup statsFor) (*NetworkStats, error) {
	containers, err := m.ListContainers(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return buildNetworkStats(containers, networkName, lookup), nil
}

// buildNetworkStats is the pure part, so the attribution rule can be tested
// without a daemon.
func buildNetworkStats(containers []ContainerSummary, networkName string, lookup statsFor) *NetworkStats {
	out := &NetworkStats{Containers: []NetworkEndpointStats{}}
	for _, c := range containers {
		if !attachedTo(c.Networks, networkName) {
			continue
		}
		out.Endpoints++
		row := NetworkEndpointStats{
			ContainerID:   c.ID,
			ContainerName: c.Name,
			// Exactly one attachment means every byte this container moved was on
			// this network. More than one and the counters are a mixture Docker
			// gives us no way to separate.
			Attributable: len(c.Networks) == 1,
		}
		if lookup != nil {
			if rx, tx, rxd, txd, rxe, txe, ok := lookup(c.ID); ok {
				row.RxBytes, row.TxBytes = rx, tx
				row.RxDropped, row.TxDropped = rxd, txd
				row.RxErrors, row.TxErrors = rxe, txe
			}
		}
		if row.Attributable {
			out.RxBytes += row.RxBytes
			out.TxBytes += row.TxBytes
			out.RxDropped += row.RxDropped
			out.TxDropped += row.TxDropped
			out.RxErrors += row.RxErrors
			out.TxErrors += row.TxErrors
		} else {
			out.Unattributed++
		}
		out.Containers = append(out.Containers, row)
	}
	sort.Slice(out.Containers, func(i, j int) bool {
		return out.Containers[i].ContainerName < out.Containers[j].ContainerName
	})
	return out
}

// attachedTo reports whether names contains network.
func attachedTo(names []string, network string) bool {
	for _, n := range names {
		if n == network {
			return true
		}
	}
	return false
}
