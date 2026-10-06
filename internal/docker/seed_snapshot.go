package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// SeedSnapshot is what a project's seed volumes held before they were reseeded,
// kept on the Docker Commander machine so a restore that fails afterwards can
// put it back.
//
// Seeding writes into the volumes the running containers mount, under stable
// names, so a restore that seeded and then failed (a later seed, the file swap,
// `compose up`) left the containers reading the old revision's files while the
// API reported a failure. The project folder was rolled back; the volumes were
// not.
type SeedSnapshot struct {
	m      *Manager
	hostID int64
	dir    string
	vols   []snapVolume
}

type snapVolume struct {
	name    string
	existed bool
	tar     string // the volume's content as CopyFromContainer returned it
}

// SnapshotSeedVolumes copies out the current content of every seed volume the
// binds use. A volume that doesn't exist yet is noted, so Restore removes it
// rather than leaving one the restore created.
func (m *Manager) SnapshotSeedVolumes(ctx context.Context, hostID int64, slug string, binds []ProjectBind) (*SeedSnapshot, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "dc-seed-snapshot-*") // 0700
	if err != nil {
		return nil, err
	}
	s := &SeedSnapshot{m: m, hostID: hostID, dir: dir}
	seen := map[string]bool{}
	for _, b := range binds {
		name := SeedVolumeName(slug, b.Rel)
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, err := cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err != nil {
			if cerrdefs.IsNotFound(err) {
				s.vols = append(s.vols, snapVolume{name: name})
				continue
			}
			s.Discard(ctx)
			return nil, fmt.Errorf("inspect seed volume %s: %w", name, err)
		}
		v := snapVolume{name: name, existed: true, tar: filepath.Join(dir, fmt.Sprintf("%d.tar", len(s.vols)))}
		if err := m.saveVolume(ctx, hostID, name, v.tar); err != nil {
			s.Discard(ctx)
			return nil, fmt.Errorf("snapshot seed volume %s: %w", name, err)
		}
		s.vols = append(s.vols, v)
	}
	return s, nil
}

// saveVolume writes the whole of a volume, as one TAR, to path.
func (m *Manager) saveVolume(ctx context.Context, hostID int64, volume, path string) error {
	rc, _, err := m.VolumeCopyFrom(ctx, hostID, volume, "/")
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Restore puts every snapshotted volume back as it was: its content replaced by
// the snapshot, owners and modes included, or removed if the restore created
// it. It carries on past a failing volume and reports every failure, so one
// volume that can't be restored doesn't leave the others reseeded.
func (s *SeedSnapshot) Restore(ctx context.Context) error {
	cli, err := s.m.Client(ctx, s.hostID)
	if err != nil {
		return err
	}
	var errs []error
	for _, v := range s.vols {
		if !v.existed {
			if _, err := cli.VolumeRemove(ctx, v.name, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("remove %s: %w", v.name, err))
			}
			continue
		}
		if err := s.m.replaceVolume(ctx, s.hostID, v.name, v.tar); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", v.name, err))
		}
	}
	for _, v := range s.vols {
		s.m.CloseVolumeBrowser(ctx, s.hostID, v.name)
	}
	return errors.Join(errs...)
}

// replaceVolume empties a volume and extracts a saved TAR of it back in.
func (m *Manager) replaceVolume(ctx context.Context, hostID int64, volume, tarPath string) error {
	id, err := m.volumeHelper(ctx, hostID, volume)
	if err != nil {
		return err
	}
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	// Everything under the mount, dotfiles included, but not the mount itself.
	_, stderr, code, err := execCapture(ctx, cli, id, []string{"find", volfsMount, "-mindepth", "1", "-delete"})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("empty the volume: %s", stderr)
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	// The TAR's root is the mount directory itself, as CopyFromContainer
	// returned it, so it is extracted at "/". The owners in the TAR are kept
	// (the integration test checks a file a container chowned).
	return copyToContainer(ctx, cli, id, "/", f)
}

// Discard removes the snapshot's local files. Call it once the restore no
// longer needs them, whatever the outcome.
func (s *SeedSnapshot) Discard(_ context.Context) {
	if s != nil && s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}
