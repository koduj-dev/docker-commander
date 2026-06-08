package docker

import (
	"context"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

// NetworkCreateRequest holds the user-supplied options for a new network.
type NetworkCreateRequest struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Subnet     string            `json:"subnet"`
	Gateway    string            `json:"gateway"`
	Internal   bool              `json:"internal"`
	Attachable bool              `json:"attachable"`
	Labels     map[string]string `json:"labels"`
}

// CreateNetwork creates a user-defined network and returns its ID.
func (m *Manager) CreateNetwork(ctx context.Context, hostID int64, req NetworkCreateRequest) (string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", err
	}
	driver := req.Driver
	if driver == "" {
		driver = "bridge"
	}
	opts := network.CreateOptions{
		Driver:     driver,
		Internal:   req.Internal,
		Attachable: req.Attachable,
		Labels:     req.Labels,
	}
	if req.Subnet != "" || req.Gateway != "" {
		opts.IPAM = &network.IPAM{Config: []network.IPAMConfig{{Subnet: req.Subnet, Gateway: req.Gateway}}}
	}
	resp, err := cli.NetworkCreate(ctx, req.Name, opts)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ConnectNetwork attaches a container to a network.
func (m *Manager) ConnectNetwork(ctx context.Context, hostID int64, netID, containerID string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.NetworkConnect(ctx, netID, containerID, nil)
}

// DisconnectNetwork detaches a container from a network (force allows removing a
// running container's endpoint).
func (m *Manager) DisconnectNetwork(ctx context.Context, hostID int64, netID, containerID string, force bool) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.NetworkDisconnect(ctx, netID, containerID, force)
}

// PruneNetworks removes all unused user-defined networks and returns the names
// the daemon deleted.
func (m *Manager) PruneNetworks(ctx context.Context, hostID int64) ([]string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	rep, err := cli.NetworksPrune(ctx, filters.NewArgs())
	if err != nil {
		return nil, err
	}
	return rep.NetworksDeleted, nil
}
