package docker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/volume"
)

// A remote daemon can't see the paths a locally-stored project bind-mounts, so a
// remote deploy translates each bind whose source lives inside the project folder
// into a named volume on the target host, seeded with a copy of those files (via
// the same helper-container path the volume browser uses). Binds pointing outside
// the project folder are refused: they name paths on the remote host, which we
// must not mount blind — they may exist there and hold something sensitive.
//
// The copy is a snapshot taken at deploy time, NOT a live mount: writes inside
// the container land in the volume and do not flow back to the project files.
const (
	// seedVolLabel marks a seeded volume with its project slug so seeds can be
	// found and cleaned up; seedRelLabel records which bind source it holds.
	seedVolLabel = "dc.seed.project"
	seedRelLabel = "dc.seed.path"
)

// ProjectBind is a host-path bind mount from a project's resolved compose config.
type ProjectBind struct {
	Service  string // compose service declaring the mount
	Source   string // host path, as compose resolved it (absolute)
	Target   string // path inside the container
	ReadOnly bool
	Rel      string // Source relative to the project dir ("." = the dir itself)
	IsFile   bool   // Source is a regular file rather than a directory
}

// String renders a bind for user-facing messages.
func (b ProjectBind) String() string {
	return fmt.Sprintf("%s: %s → %s", b.Service, b.Source, b.Target)
}

// ClassifyProjectBinds splits the bind mounts in a resolved compose config
// (`docker compose config --format json`) into those whose source lives inside
// projectDir — which a remote deploy can ship as a seeded volume — and those that
// don't, which it must refuse. Symlinks are resolved before the containment test,
// so a link inside the project pointing out of it counts as external.
func ClassifyProjectBinds(configJSON []byte, projectDir string) (internal, external []ProjectBind, err error) {
	var cfg struct {
		Services map[string]struct {
			Volumes []struct {
				Type     string `json:"type"`
				Source   string `json:"source"`
				Target   string `json:"target"`
				ReadOnly bool   `json:"read_only"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, nil, err
	}
	root, err := canonicalDir(projectDir)
	if err != nil {
		return nil, nil, err
	}
	// Iterate services in a stable order so the override and any error message
	// are deterministic.
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, v := range cfg.Services[name].Volumes {
			if v.Type != "bind" {
				continue
			}
			b := ProjectBind{Service: name, Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly}
			rel, ok := relWithin(root, v.Source)
			if !ok {
				external = append(external, b)
				continue
			}
			b.Rel = rel
			if fi, serr := os.Stat(v.Source); serr == nil {
				b.IsFile = fi.Mode().IsRegular()
			}
			internal = append(internal, b)
		}
	}
	return internal, external, nil
}

// canonicalDir returns dir as an absolute, symlink-resolved path.
func canonicalDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("project dir %q: %w", dir, err)
	}
	return resolved, nil
}

// relWithin reports whether p resolves to root or something under it, returning
// the path relative to root. Both are canonicalised first — including the deepest
// existing ancestor of p, so a symlinked parent can't smuggle the path out of
// root, and a not-yet-created source still classifies correctly.
func relWithin(root, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	resolved := resolveExisting(abs)
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// resolveExisting canonicalises the longest existing prefix of abs and re-appends
// the components that don't exist yet.
func resolveExisting(abs string) string {
	rest := ""
	cur := filepath.Clean(abs)
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return resolved
			}
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur { // reached the root without finding an existing path
			return filepath.Clean(abs)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// SeedVolumeName derives the deterministic name of the volume that carries a
// project's bind source on a remote host. The relative path is hashed so any
// path shape yields a valid, collision-resistant Docker volume name.
func SeedVolumeName(slug, rel string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(rel))))
	return fmt.Sprintf("dcseed-%s-%s", sanitizeVolumeSlug(slug), hex.EncodeToString(sum[:])[:12])
}

// sanitizeVolumeSlug reduces a project slug to characters Docker accepts in a
// volume name, so a seeded volume name is always valid.
func sanitizeVolumeSlug(slug string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(slug) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	s := strings.Trim(b.String(), "-_.")
	if s == "" {
		return "project"
	}
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-_.")
	}
	return s
}

// BindOverrideJSON builds a compose override that repoints each bind at its
// seeded volume. It's emitted as JSON (valid YAML, and no new dependency): compose
// merges service volumes by container target, so listing only the targets we
// replace leaves every other mount untouched. The volumes are declared external
// so compose uses the exact names we created rather than prefixing them.
func BindOverrideJSON(slug string, binds []ProjectBind) ([]byte, error) {
	type volSpec struct {
		Subpath string `json:"subpath,omitempty"`
	}
	type mountSpec struct {
		Type     string   `json:"type"`
		Source   string   `json:"source"`
		Target   string   `json:"target"`
		ReadOnly bool     `json:"read_only,omitempty"`
		Volume   *volSpec `json:"volume,omitempty"`
	}
	type serviceSpec struct {
		Volumes []mountSpec `json:"volumes"`
	}
	type topVolume struct {
		External bool `json:"external"`
	}
	out := struct {
		Services map[string]serviceSpec `json:"services"`
		Volumes  map[string]topVolume   `json:"volumes"`
	}{
		Services: map[string]serviceSpec{},
		Volumes:  map[string]topVolume{},
	}
	for _, b := range binds {
		name := SeedVolumeName(slug, b.Rel)
		m := mountSpec{Type: "volume", Source: name, Target: b.Target, ReadOnly: b.ReadOnly}
		if b.IsFile {
			// A volume holds a directory, so a single-file bind mounts the file
			// out of it by subpath (the seed tar stores it under its base name).
			m.Volume = &volSpec{Subpath: filepath.Base(b.Rel)}
		}
		svc := out.Services[b.Service]
		svc.Volumes = append(svc.Volumes, m)
		out.Services[b.Service] = svc
		out.Volumes[name] = topVolume{External: true}
	}
	return json.MarshalIndent(out, "", "  ")
}

// SeedProjectBinds creates and fills the volume behind each internal bind on the
// target host: the local files are streamed in as a TAR through a helper
// container. Existing content is overwritten so a redeploy ships current files.
func (m *Manager) SeedProjectBinds(ctx context.Context, hostID int64, projectDir, slug string, binds []ProjectBind) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	for _, b := range binds {
		name := SeedVolumeName(slug, b.Rel)
		if _, err := cli.VolumeCreate(ctx, volume.CreateOptions{
			Name: name,
			Labels: map[string]string{
				seedVolLabel: slug,
				seedRelLabel: filepath.ToSlash(b.Rel),
			},
		}); err != nil {
			return fmt.Errorf("create seed volume for %s: %w", b.Rel, err)
		}
		src := filepath.Join(projectDir, b.Rel)
		tarball, err := tarPath(src, b.IsFile)
		if err != nil {
			return fmt.Errorf("archive %s: %w", b.Rel, err)
		}
		if err := m.VolumeCopyTo(ctx, hostID, name, "/", tarball); err != nil {
			return fmt.Errorf("seed volume for %s: %w", b.Rel, err)
		}
	}
	if len(binds) > 0 {
		// The helpers have served their purpose; leaving them running would show
		// up as stray containers on the remote host until the TTL reaps them.
		for _, b := range binds {
			m.CloseVolumeBrowser(ctx, hostID, SeedVolumeName(slug, b.Rel))
		}
	}
	return nil
}

// tarPath archives src for extraction at a volume's root. A single file is stored
// under its base name; a directory's contents are stored at the top level (so the
// volume mirrors the directory, not a nested copy of it). Symlinks are stored as
// links, never followed, so a link inside the project can't pull in outside files.
func tarPath(src string, isFile bool) (io.Reader, error) {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := writeTar(tw, src, isFile)
		if cerr := tw.Close(); err == nil {
			err = cerr
		}
		_ = pw.CloseWithError(err)
	}()
	return pr, nil
}

func writeTar(tw *tar.Writer, src string, isFile bool) error {
	// A compose file may name a bind source the project doesn't contain yet (e.g.
	// ./data for a database that creates it on first run). Locally Docker would
	// materialise it on demand, so the remote equivalent is an empty volume — not
	// a failed deploy. Emit an empty archive rather than the lstat error.
	if _, err := os.Lstat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if isFile {
		return tarOne(tw, src, filepath.Base(src))
	}
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil // the volume root already exists
		}
		return tarOne(tw, p, filepath.ToSlash(rel))
	})
}

// tarOne writes a single filesystem entry. Regular files, directories and
// symlinks are supported; anything else (socket, device, fifo) is skipped rather
// than failing the whole deploy.
func tarOne(tw *tar.Writer, p, name string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	var link string
	if fi.Mode()&os.ModeSymlink != 0 {
		if link, err = os.Readlink(p); err != nil {
			return err
		}
	} else if !fi.Mode().IsRegular() && !fi.IsDir() {
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	hdr.Name = name
	if fi.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
		hdr.Name += "/"
	}
	// Drop uid/gid/uname/gname: the archive is extracted on another host where
	// those ids mean something different, and numeric owners would leak.
	hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
