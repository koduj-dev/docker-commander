package docker

import (
	"context"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// ListContainers returns a compact summary of all containers on the host.
func (m *Manager) ListContainers(ctx context.Context, hostID int64) ([]ContainerSummary, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	raw, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	out := make([]ContainerSummary, 0, len(raw))
	for _, c := range raw {
		// Hide internal volume-browser helper containers from every view.
		if c.Labels[volfsLabel] != "" {
			continue
		}
		s := ContainerSummary{
			ID:      c.ID,
			Name:    cleanName(c.Names),
			Image:   c.Image,
			State:   string(c.State),
			Status:  c.Status,
			Created: c.Created,
			Labels:  c.Labels,
		}
		for _, p := range c.Ports {
			s.Ports = append(s.Ports, PortMapping{
				IP: p.IP, PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Type: p.Type,
			})
		}
		if c.NetworkSettings != nil {
			for name := range c.NetworkSettings.Networks {
				s.Networks = append(s.Networks, name)
			}
		}
		out = append(out, s)
	}
	// Running first, then alphabetical within each group.
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].State == "running", out[j].State == "running"
		if ri != rj {
			return ri
		}
		if c := cmpFold(out[i].Name, out[j].Name); c != 0 {
			return c < 0
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// cmpFold compares two names case-insensitively, then (on a fold-tie)
// case-sensitively, returning -1/0/1. Callers add an ID tie-breaker for the
// final 0 case so ordering is deterministic regardless of the daemon's order
// (a plain folded compare leaves names differing only by case in arbitrary
// order under a stable sort).
func cmpFold(a, b string) int {
	if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
		if la < lb {
			return -1
		}
		return 1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// InspectContainer returns the detailed view of a single container.
func (m *Manager) InspectContainer(ctx context.Context, hostID int64, id string) (*ContainerDetail, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	c, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, err
	}

	d := &ContainerDetail{
		ID:      c.ID,
		Name:    strings.TrimPrefix(c.Name, "/"),
		Created: c.Created,
		Command: append([]string{c.Path}, c.Args...),
	}
	if c.Config != nil {
		d.Image = c.Config.Image
		d.Env = c.Config.Env
		d.Labels = c.Config.Labels
	}
	if c.State != nil {
		d.State = c.State.Status
		d.Status = c.State.Status
		d.StartedAt = c.State.StartedAt
		d.RestartCount = c.RestartCount
		if c.State.Health != nil {
			d.Health = c.State.Health.Status
		}
	}
	if c.HostConfig != nil {
		d.RestartPolicy = string(c.HostConfig.RestartPolicy.Name)
	}
	for _, mnt := range c.Mounts {
		d.Mounts = append(d.Mounts, MountInfo{
			Type: string(mnt.Type), Source: mnt.Source, Destination: mnt.Destination, RW: mnt.RW,
		})
	}
	if c.NetworkSettings != nil {
		for name, ep := range c.NetworkSettings.Networks {
			d.Networks = append(d.Networks, NetworkAttach{
				Name: name, NetworkID: ep.NetworkID, IPAddress: ep.IPAddress,
				Gateway: ep.Gateway, MacAddress: ep.MacAddress,
			})
		}
	}
	return d, nil
}

// ContainerAction performs a lifecycle action: start, stop, restart, pause,
// unpause, kill. Unknown actions return an error.
func (m *Manager) ContainerAction(ctx context.Context, hostID int64, id, action string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		return cli.ContainerStart(ctx, id, container.StartOptions{})
	case "stop":
		return cli.ContainerStop(ctx, id, container.StopOptions{})
	case "restart":
		return cli.ContainerRestart(ctx, id, container.StopOptions{})
	case "pause":
		return cli.ContainerPause(ctx, id)
	case "unpause":
		return cli.ContainerUnpause(ctx, id)
	case "kill":
		return cli.ContainerKill(ctx, id, "KILL")
	default:
		return ErrUnknownAction
	}
}

// ListNetworks returns networks plus the set of containers attached to each,
// which the frontend uses to draw the connectivity topology.
func (m *Manager) ListNetworks(ctx context.Context, hostID int64) ([]NetworkSummary, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	nets, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := make([]NetworkSummary, 0, len(nets))
	for _, n := range nets {
		// NetworkList is shallow; inspect to learn attached containers + IPAM.
		full, err := cli.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if err != nil {
			continue
		}
		ns := NetworkSummary{
			ID: full.ID, Name: full.Name, Driver: full.Driver,
			Scope: full.Scope, Internal: full.Internal,
		}
		for _, cfg := range full.IPAM.Config {
			if cfg.Subnet != "" {
				ns.Subnets = append(ns.Subnets, cfg.Subnet)
			}
		}
		for cid := range full.Containers {
			ns.Containers = append(ns.Containers, cid)
		}
		out = append(out, ns)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := cmpFold(out[i].Name, out[j].Name); c != 0 {
			return c < 0
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// RemoveNetwork deletes a user-defined network. The daemon refuses to remove
// predefined networks (bridge/host/none) or ones with attached endpoints, and
// that error is surfaced to the caller.
func (m *Manager) RemoveNetwork(ctx context.Context, hostID int64, id string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.NetworkRemove(ctx, id)
}

// SystemInfo returns a summary of the Docker host.
// Ping reports whether a host's Docker daemon is reachable: nil if the daemon
// answers, or the dial/ping error otherwise. It is cheaper than SystemInfo (no
// full Info call) and is used by the monitor's health loop.
func (m *Manager) Ping(ctx context.Context, hostID int64) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	_, err = cli.Ping(ctx)
	return err
}

func (m *Manager) SystemInfo(ctx context.Context, hostID int64) (*SystemInfo, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	info, err := cli.Info(ctx)
	if err != nil {
		return nil, err
	}
	return &SystemInfo{
		HostName:          info.Name,
		ServerVersion:     info.ServerVersion,
		OperatingSystem:   info.OperatingSystem,
		OSType:            info.OSType,
		OSVersion:         info.OSVersion,
		KernelVersion:     info.KernelVersion,
		Architecture:      info.Architecture,
		CPUs:              info.NCPU,
		MemTotal:          info.MemTotal,
		StorageDriver:     info.Driver,
		LoggingDriver:     info.LoggingDriver,
		CgroupDriver:      info.CgroupDriver,
		CgroupVersion:     info.CgroupVersion,
		DockerRootDir:     info.DockerRootDir,
		LiveRestore:       info.LiveRestoreEnabled,
		Containers:        info.Containers,
		ContainersRunning: info.ContainersRunning,
		ContainersPaused:  info.ContainersPaused,
		ContainersStopped: info.ContainersStopped,
		Images:            info.Images,
	}, nil
}

// cleanName strips the leading slash Docker adds to container names and returns
// the first (primary) name.
func cleanName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}
