package docker

import (
	"context"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
)

// VolumeSummary describes a Docker volume for the Volumes page, including which
// containers currently mount it so removal is informed.
type VolumeSummary struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	CreatedAt  string            `json:"createdAt"`
	Labels     map[string]string `json:"labels"`
	InUseBy    []string          `json:"inUseBy"` // container names mounting this volume
}

// VolumePruneResult reports what an unused-volume prune removed.
type VolumePruneResult struct {
	Deleted        []string `json:"deleted"`
	SpaceReclaimed uint64   `json:"spaceReclaimed"`
}

// ListVolumes returns all volumes, cross-referencing containers' mounts so the
// UI can show (and warn about) volumes that are still in use.
func (m *Manager) ListVolumes(ctx context.Context, hostID int64) ([]VolumeSummary, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Map volume name -> container names that mount it.
	usage := map[string][]string{}
	if ctrs, err := cli.ContainerList(ctx, container.ListOptions{All: true}); err == nil {
		for _, c := range ctrs {
			name := cleanName(c.Names)
			for _, mnt := range c.Mounts {
				if mnt.Type == "volume" && mnt.Name != "" {
					usage[mnt.Name] = append(usage[mnt.Name], name)
				}
			}
		}
	}

	out := make([]VolumeSummary, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		if v == nil {
			continue
		}
		out = append(out, VolumeSummary{
			Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint,
			Scope: v.Scope, CreatedAt: v.CreatedAt, Labels: v.Labels,
			InUseBy: usage[v.Name],
		})
	}
	return out, nil
}

// CreateVolume creates a named volume with an optional driver.
func (m *Manager) CreateVolume(ctx context.Context, hostID int64, name, driver string, labels map[string]string) (*VolumeSummary, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	v, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: name, Driver: driver, Labels: labels})
	if err != nil {
		return nil, err
	}
	return &VolumeSummary{
		Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint,
		Scope: v.Scope, CreatedAt: v.CreatedAt, Labels: v.Labels,
	}, nil
}

// RemoveVolume deletes a volume. force removes it even if the metadata claims a
// reference; the daemon still refuses volumes actively mounted by a container.
func (m *Manager) RemoveVolume(ctx context.Context, hostID int64, name string, force bool) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.VolumeRemove(ctx, name, force)
}

// PruneVolumes removes all unused (anonymous and named) volumes.
func (m *Manager) PruneVolumes(ctx context.Context, hostID int64) (*VolumePruneResult, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	// "all=true" prunes named volumes too, not just anonymous ones.
	rep, err := cli.VolumesPrune(ctx, filters.NewArgs(filters.Arg("all", "true")))
	if err != nil {
		return nil, err
	}
	return &VolumePruneResult{Deleted: rep.VolumesDeleted, SpaceReclaimed: rep.SpaceReclaimed}, nil
}
