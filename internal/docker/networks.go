package docker

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
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
	opts := client.NetworkCreateOptions{
		Driver:     driver,
		Internal:   req.Internal,
		Attachable: req.Attachable,
		Labels:     req.Labels,
	}
	if req.Subnet != "" || req.Gateway != "" {
		// The SDK now carries these as typed values, so a malformed one is
		// rejected here with a message naming the field, instead of by the
		// daemon after a round trip.
		var cfg network.IPAMConfig
		if req.Subnet != "" {
			p, err := netip.ParsePrefix(req.Subnet)
			if err != nil {
				return "", fmt.Errorf("invalid subnet %q: %w", req.Subnet, err)
			}
			cfg.Subnet = p
		}
		if req.Gateway != "" {
			a, err := netip.ParseAddr(req.Gateway)
			if err != nil {
				return "", fmt.Errorf("invalid gateway %q: %w", req.Gateway, err)
			}
			cfg.Gateway = a
		}
		opts.IPAM = &network.IPAM{Config: []network.IPAMConfig{cfg}}
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
	_, err = cli.NetworkConnect(ctx, netID, client.NetworkConnectOptions{Container: containerID})
	return err
}

// DisconnectNetwork detaches a container from a network (force allows removing a
// running container's endpoint).
func (m *Manager) DisconnectNetwork(ctx context.Context, hostID int64, netID, containerID string, force bool) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	_, err = cli.NetworkDisconnect(ctx, netID, client.NetworkDisconnectOptions{Container: containerID, Force: force})
	return err
}

// PruneNetworks removes all unused user-defined networks and returns the names
// the daemon deleted.
func (m *Manager) PruneNetworks(ctx context.Context, hostID int64) ([]string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	res, err := cli.NetworkPrune(ctx, client.NetworkPruneOptions{})
	if err != nil {
		return nil, err
	}
	return res.Report.NetworksDeleted, nil
}
