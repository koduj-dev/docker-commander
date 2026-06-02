package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

// PortSpec is one published port in a create request.
type PortSpec struct {
	HostPort      string `json:"hostPort"`      // empty = expose only, no host binding
	ContainerPort string `json:"containerPort"` // required
	Proto         string `json:"proto"`         // tcp (default) | udp
}

// CreateSpec is the user-facing container create/run request.
type CreateSpec struct {
	Image         string     `json:"image"`
	Name          string     `json:"name"`
	Cmd           []string   `json:"cmd"`
	Env           []string   `json:"env"`   // KEY=VALUE
	Binds         []string   `json:"binds"` // src:dst[:ro]
	Ports         []PortSpec `json:"ports"`
	RestartPolicy string     `json:"restartPolicy"` // "", no, always, unless-stopped, on-failure
	Memory        int64      `json:"memory"`        // bytes, 0 = unset
	NanoCPUs      int64      `json:"nanoCpus"`      // 0 = unset (cpus * 1e9)
	Start         bool       `json:"start"`
}

// CreateContainer creates (and optionally starts) a container from a spec.
func (m *Manager) CreateContainer(ctx context.Context, hostID int64, spec CreateSpec) (string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", err
	}
	if spec.Image == "" {
		return "", fmt.Errorf("image is required")
	}

	cfg := &container.Config{Image: spec.Image, Env: spec.Env, ExposedPorts: nat.PortSet{}}
	if len(spec.Cmd) > 0 {
		cfg.Cmd = spec.Cmd
	}
	hostCfg := &container.HostConfig{Binds: spec.Binds, PortBindings: nat.PortMap{}}

	for _, p := range spec.Ports {
		if p.ContainerPort == "" {
			continue
		}
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		port, err := nat.NewPort(proto, p.ContainerPort)
		if err != nil {
			return "", fmt.Errorf("invalid port %q: %w", p.ContainerPort, err)
		}
		cfg.ExposedPorts[port] = struct{}{}
		if p.HostPort != "" {
			hostCfg.PortBindings[port] = []nat.PortBinding{{HostPort: p.HostPort}}
		}
	}
	if spec.RestartPolicy != "" {
		hostCfg.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(spec.RestartPolicy)}
	}
	hostCfg.Memory = spec.Memory
	hostCfg.NanoCPUs = spec.NanoCPUs
	if spec.Memory > 0 {
		hostCfg.MemorySwap = spec.Memory // no extra swap beyond the memory limit
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, spec.Name)
	if err != nil {
		return "", err
	}
	if spec.Start {
		if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			return resp.ID, err
		}
	}
	return resp.ID, nil
}

// RenameContainer changes a container's name.
func (m *Manager) RenameContainer(ctx context.Context, hostID int64, id, newName string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.ContainerRename(ctx, id, newName)
}

// UpdateContainer adjusts a running container's resource limits and restart
// policy at runtime. Zero values leave the corresponding limit unchanged-as-set.
func (m *Manager) UpdateContainer(ctx context.Context, hostID int64, id string, memory, nanoCPUs int64, restartPolicy string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	uc := container.UpdateConfig{}
	uc.Memory = memory
	uc.NanoCPUs = nanoCPUs
	// Setting a memory limit without a matching memory+swap limit is rejected by
	// the daemon ("smaller than already set memoryswap"); pin swap to the memory
	// limit (i.e. no extra swap) so the update is accepted.
	if memory > 0 {
		uc.MemorySwap = memory
	}
	if restartPolicy != "" {
		uc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(restartPolicy)}
	}
	_, err = cli.ContainerUpdate(ctx, id, uc)
	return err
}

// CommitContainer snapshots a container into a new image (reference repo:tag).
func (m *Manager) CommitContainer(ctx context.Context, hostID int64, id, ref, comment string) (string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", err
	}
	resp, err := cli.ContainerCommit(ctx, id, container.CommitOptions{
		Reference: ref, Comment: comment, Pause: true,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}
