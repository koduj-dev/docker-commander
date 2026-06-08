package docker

import (
	"context"
	"io"
	"path"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

// Named volumes have no path reachable through the Docker API, so to browse one
// we run a tiny helper container with the volume mounted at /data and reuse the
// in-container file operations (exec ls/rm + docker cp) against it. Helpers are
// labelled so we can find, hide (ListContainers skips them) and reap them.
const (
	volfsLabel = "dc.volfs"     // value = the volume name
	volfsImage = "busybox:1.37" // tiny; has ls / rm / sh. Pinned (not :latest) so volume browsing is deterministic across installs.
	volfsMount = "/data"
	volfsTTL   = 2 * time.Hour // reap helpers older than this
)

// volumeHelper ensures a running helper container for a volume and returns its
// ID, creating it (and pulling busybox) on first use. Stale helpers are reaped.
func (m *Manager) volumeHelper(ctx context.Context, hostID int64, volume string) (string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", err
	}
	reapVolumeHelpers(ctx, cli, true) // best-effort: drop helpers older than the TTL

	existing, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", volfsLabel+"="+volume)),
	})
	if err != nil {
		return "", err
	}
	for _, c := range existing {
		if c.State == "running" {
			return c.ID, nil
		}
		if err := cli.ContainerStart(ctx, c.ID, container.StartOptions{}); err == nil {
			return c.ID, nil
		}
		_ = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}

	if err := ensureHelperImage(ctx, cli, volfsImage); err != nil {
		return "", err
	}
	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:  volfsImage,
			Cmd:    []string{"sleep", "2147483647"},
			Labels: map[string]string{volfsLabel: volume},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: volfsMount}},
		}, nil, nil, "")
	if err != nil {
		return "", err
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", err
	}
	return resp.ID, nil
}

// ensureHelperImage pulls ref if it isn't present locally.
func ensureHelperImage(ctx context.Context, cli *client.Client, ref string) error {
	if _, _, err := cli.ImageInspectWithRaw(ctx, ref); err == nil {
		return nil
	}
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, _ = io.Copy(io.Discard, rc) // drain so the pull completes
	return nil
}

// reapVolumeHelpers removes volume-browser helper containers. With onlyOld it
// removes only those older than the TTL (lazy cleanup); otherwise all of them
// (used at startup to clear orphans from a previous run).
func reapVolumeHelpers(ctx context.Context, cli *client.Client, onlyOld bool) {
	list, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", volfsLabel)),
	})
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-volfsTTL).Unix()
	for _, c := range list {
		if onlyOld && c.Created > cutoff {
			continue
		}
		_ = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}
}

// volPath maps a user path (relative to the volume root) into the helper's
// mount, jailing traversal.
func volPath(p string) string {
	clean := path.Clean("/" + strings.TrimSpace(p)) // collapses ".." against "/"
	if clean == "/" {
		return volfsMount
	}
	return volfsMount + clean
}

// VolumeListPath lists one directory level inside a volume.
func (m *Manager) VolumeListPath(ctx context.Context, hostID int64, volume, p string) ([]FileEntry, error) {
	id, err := m.volumeHelper(ctx, hostID, volume)
	if err != nil {
		return nil, err
	}
	return m.ListPath(ctx, hostID, id, volPath(p))
}

// VolumeCopyFrom streams a path out of a volume as a TAR archive.
func (m *Manager) VolumeCopyFrom(ctx context.Context, hostID int64, volume, p string) (io.ReadCloser, container.PathStat, error) {
	id, err := m.volumeHelper(ctx, hostID, volume)
	if err != nil {
		return nil, container.PathStat{}, err
	}
	return m.CopyFrom(ctx, hostID, id, volPath(p))
}

// VolumeCopyTo writes a TAR archive into a volume directory.
func (m *Manager) VolumeCopyTo(ctx context.Context, hostID int64, volume, destDir string, content io.Reader) error {
	id, err := m.volumeHelper(ctx, hostID, volume)
	if err != nil {
		return err
	}
	return m.CopyTo(ctx, hostID, id, volPath(destDir), content)
}

// VolumeDeletePath removes a path inside a volume.
func (m *Manager) VolumeDeletePath(ctx context.Context, hostID int64, volume, p string) error {
	id, err := m.volumeHelper(ctx, hostID, volume)
	if err != nil {
		return err
	}
	return m.DeletePath(ctx, hostID, id, volPath(p))
}

// VolumeMakeDir creates a directory inside a volume.
func (m *Manager) VolumeMakeDir(ctx context.Context, hostID int64, volume, p string) error {
	id, err := m.volumeHelper(ctx, hostID, volume)
	if err != nil {
		return err
	}
	return m.MakeDir(ctx, hostID, id, volPath(p))
}

// VolumeUploadExtract extracts an archive into a volume directory.
func (m *Manager) VolumeUploadExtract(ctx context.Context, hostID int64, volume, destDir, filename string, body io.Reader) error {
	id, err := m.volumeHelper(ctx, hostID, volume)
	if err != nil {
		return err
	}
	return m.UploadExtract(ctx, hostID, id, volPath(destDir), filename, body)
}

// CloseVolumeBrowser removes the helper container(s) for a volume (called when
// the user closes the browser).
func (m *Manager) CloseVolumeBrowser(ctx context.Context, hostID int64, volume string) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return
	}
	list, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", volfsLabel+"="+volume)),
	})
	if err != nil {
		return
	}
	for _, c := range list {
		_ = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}
}

// ReapAllVolumeHelpers clears every volume-browser helper on the default host
// (called at startup to remove orphans from a previous run).
func (m *Manager) ReapAllVolumeHelpers(ctx context.Context) {
	if cli, err := m.Client(ctx, 0); err == nil {
		reapVolumeHelpers(ctx, cli, false)
	}
}
