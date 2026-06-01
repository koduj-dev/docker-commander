package docker

import (
	"context"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// Topology is a graph view of how containers attach to networks, consumed by
// the frontend connectivity diagram.
type Topology struct {
	Networks   []TopoNetwork   `json:"networks"`
	Containers []TopoContainer `json:"containers"`
	Links      []TopoLink      `json:"links"`
}

// TopoNetwork is a network node in the topology graph.
type TopoNetwork struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Driver   string   `json:"driver"`
	Scope    string   `json:"scope"`
	Internal bool     `json:"internal"`
	Subnets  []string `json:"subnets"`
}

// TopoContainer is a container node in the topology graph.
type TopoContainer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	State string `json:"state"`
}

// TopoLink is an edge: a container attached to a network with an assigned IP.
type TopoLink struct {
	ContainerID string `json:"containerId"`
	NetworkID   string `json:"networkId"`
	IPAddress   string `json:"ipAddress"`
}

// Topology builds the container↔network connectivity graph for a host.
func (m *Manager) Topology(ctx context.Context, hostID int64) (*Topology, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}

	rawContainers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	// Track which containers actually exist so we never emit a dangling edge.
	known := make(map[string]bool, len(rawContainers))

	top := &Topology{}
	for _, c := range rawContainers {
		known[c.ID] = true
		top.Containers = append(top.Containers, TopoContainer{
			ID: c.ID, Name: cleanName(c.Names), Image: c.Image, State: string(c.State),
		})
	}

	nets, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range nets {
		full, err := cli.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if err != nil {
			continue
		}
		tn := TopoNetwork{
			ID: full.ID, Name: full.Name, Driver: full.Driver,
			Scope: full.Scope, Internal: full.Internal,
		}
		for _, cfg := range full.IPAM.Config {
			if cfg.Subnet != "" {
				tn.Subnets = append(tn.Subnets, cfg.Subnet)
			}
		}
		top.Networks = append(top.Networks, tn)

		for cid, ep := range full.Containers {
			if !known[cid] {
				continue // endpoint for a container not in our list (e.g. removed)
			}
			top.Links = append(top.Links, TopoLink{
				ContainerID: cid, NetworkID: full.ID, IPAddress: ep.IPv4Address,
			})
		}
	}
	return top, nil
}
