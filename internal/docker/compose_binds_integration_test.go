package docker

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// SeedProjectBinds is the piece that actually ships a project's files, so drive
// it against a real daemon: seed a directory bind and a single-file bind, then
// read the volumes back to prove the layout a remote container would see. Only
// the volumes this test creates are touched — never a host-global prune.
func TestSeedProjectBinds_Integration(t *testing.T) {
	m, ctx := newManager(t)

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "html", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(projectDir, "html", "index.html"), "<h1>hello</h1>")
	writeFile(t, filepath.Join(projectDir, "html", "nested", "app.js"), "console.log(1)")
	writeFile(t, filepath.Join(projectDir, "nginx.conf"), "worker_processes 1;")

	const slug = "seed-integration"
	binds := []ProjectBind{
		{Service: "web", Target: "/usr/share/nginx/html", Rel: "html"},
		{Service: "web", Target: "/etc/nginx/nginx.conf", Rel: "nginx.conf", IsFile: true},
	}

	// Clean up only our own volumes, whatever the outcome.
	t.Cleanup(func() {
		for _, b := range binds {
			removeSeedVolume(t, m, 0, SeedVolumeName(slug, b.Rel))
		}
	})

	if err := m.SeedProjectBinds(ctx, 0, projectDir, slug, binds); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	// The directory bind: contents at the volume root, nesting preserved.
	htmlVol := SeedVolumeName(slug, "html")
	assertVolumeLabels(ctx, t, cli, htmlVol, slug, "html")
	entries, err := m.VolumeListPath(ctx, 0, htmlVol, "/")
	if err != nil {
		t.Fatalf("list %s: %v", htmlVol, err)
	}
	names := entryNames(entries)
	if !names["index.html"] {
		t.Errorf("index.html should sit at the volume root, got %v", keys(names))
	}
	if names["html"] {
		t.Errorf("the directory must not be nested inside the volume, got %v", keys(names))
	}
	nested, err := m.VolumeListPath(ctx, 0, htmlVol, "/nested")
	if err != nil {
		t.Fatalf("list nested: %v", err)
	}
	if !entryNames(nested)["app.js"] {
		t.Errorf("nested files should be preserved, got %v", keys(entryNames(nested)))
	}

	// The single-file bind: stored under its base name so the override's
	// `subpath: nginx.conf` resolves.
	confVol := SeedVolumeName(slug, "nginx.conf")
	assertVolumeLabels(ctx, t, cli, confVol, slug, "nginx.conf")
	confEntries, err := m.VolumeListPath(ctx, 0, confVol, "/")
	if err != nil {
		t.Fatalf("list %s: %v", confVol, err)
	}
	if !entryNames(confEntries)["nginx.conf"] {
		t.Errorf("the file should be stored under its base name, got %v", keys(entryNames(confEntries)))
	}

	// Re-seeding must be idempotent — a redeploy ships current files, not an error.
	writeFile(t, filepath.Join(projectDir, "html", "index.html"), "<h1>updated</h1>")
	if err := m.SeedProjectBinds(ctx, 0, projectDir, slug, binds); err != nil {
		t.Fatalf("re-seeding (redeploy) failed: %v", err)
	}
	rc, _, err := m.VolumeCopyFrom(ctx, 0, htmlVol, "/index.html")
	if err != nil {
		t.Fatalf("read back index.html: %v", err)
	}
	defer rc.Close()
	if body := readTarEntry(t, rc, "index.html"); !strings.Contains(body, "updated") {
		t.Errorf("a redeploy should ship the current file, got %q", body)
	}
}

// Seeding a bind source that doesn't exist yet must produce an empty volume, not
// a failed deploy — a compose file may name a path the container creates itself.
// The unit test covers the archive; this proves the whole seed path against a real
// daemon, which is the gap that let the bug through.
func TestSeedProjectBinds_MissingSourceSeedsEmptyVolume(t *testing.T) {
	m, ctx := newManager(t)
	projectDir := t.TempDir()

	const slug = "seed-missing-source"
	binds := []ProjectBind{{Service: "db", Target: "/var/lib/postgresql/data", Rel: "data"}}
	name := SeedVolumeName(slug, binds[0].Rel)
	t.Cleanup(func() { removeSeedVolume(t, m, 0, name) })

	if err := m.SeedProjectBinds(ctx, 0, projectDir, slug, binds); err != nil {
		t.Fatalf("seeding a not-yet-created bind source must succeed: %v", err)
	}
	assertVolumeLabels(ctx, t, mustClient(t, m, 0), name, slug, "data")
	entries, err := m.VolumeListPath(ctx, 0, name, "/")
	if err != nil {
		t.Fatalf("list %s: %v", name, err)
	}
	if len(entries) != 0 {
		t.Errorf("the volume should be empty, got %v", keys(entryNames(entries)))
	}
}

func mustClient(t *testing.T, m *Manager, hostID int64) *client.Client {
	t.Helper()
	cli, err := m.Client(context.Background(), hostID)
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// Seed volumes are found and removed by label, scoped to one project. Critically
// this must never touch anything else on the daemon — no host-global prune — so
// the test plants a decoy volume and a second project's seeds and checks both
// survive.
func TestListAndRemoveSeedVolumes_Integration(t *testing.T) {
	m, ctx := newManager(t)
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "html", "index.html"), "hi")
	binds := []ProjectBind{{Service: "web", Target: "/usr/share/nginx/html", Rel: "html"}}

	const mine, other = "seedvol-mine", "seedvol-other"
	const decoy = "dcseed-not-a-seed-volume-decoy"
	t.Cleanup(func() {
		removeSeedVolume(t, m, 0, SeedVolumeName(mine, "html"))
		removeSeedVolume(t, m, 0, SeedVolumeName(other, "html"))
		_ = cli.VolumeRemove(context.Background(), decoy, true)
	})

	if err := m.SeedProjectBinds(ctx, 0, dir, mine, binds); err != nil {
		t.Fatal(err)
	}
	if err := m.SeedProjectBinds(ctx, 0, dir, other, binds); err != nil {
		t.Fatal(err)
	}
	// A volume whose NAME looks like a seed but carries no seed label.
	if _, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: decoy}); err != nil {
		t.Fatal(err)
	}

	names, err := m.ListSeedVolumes(ctx, 0, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != SeedVolumeName(mine, "html") {
		t.Fatalf("listing should return only this project's seeds, got %v", names)
	}

	removed, err := m.RemoveSeedVolumes(ctx, 0, mine)
	if err != nil {
		t.Fatalf("removing this project's seeds: %v", err)
	}
	if len(removed) != 1 {
		t.Errorf("expected 1 volume removed, got %v", removed)
	}
	if _, err := cli.VolumeInspect(ctx, SeedVolumeName(mine, "html")); err == nil {
		t.Error("the seed volume should be gone")
	}
	// The other project's seed and the unlabelled decoy must be untouched.
	if _, err := cli.VolumeInspect(ctx, SeedVolumeName(other, "html")); err != nil {
		t.Errorf("SECURITY/BUG: another project's seed volume was removed: %v", err)
	}
	if _, err := cli.VolumeInspect(ctx, decoy); err != nil {
		t.Errorf("SECURITY/BUG: an unlabelled volume with a seed-like name was removed: %v", err)
	}

	// Removing again is a no-op, not an error — the delete flow may retry.
	if removed, err := m.RemoveSeedVolumes(ctx, 0, mine); err != nil || len(removed) != 0 {
		t.Errorf("second removal should be a quiet no-op, got %v (err %v)", removed, err)
	}
}

// A project with no internal binds must not create any volume.
func TestSeedProjectBinds_NoBindsIsNoop(t *testing.T) {
	m, ctx := newManager(t)
	if err := m.SeedProjectBinds(ctx, 0, t.TempDir(), "seed-noop", nil); err != nil {
		t.Errorf("seeding nothing should succeed quietly: %v", err)
	}
}

// removeSeedVolume deletes a volume this test created, retrying briefly: tearing
// down the container that used it is asynchronous, so an immediate remove can hit
// "volume is in use" — which `force` does not override. Failing to clean up would
// leave volumes behind on a real daemon, so this reports rather than ignoring it.
func removeSeedVolume(t *testing.T, m *Manager, hostID int64, name string) {
	t.Helper()
	ctx := context.Background()
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		t.Logf("cleanup: no client for host %d: %v", hostID, err)
		return
	}
	m.CloseVolumeBrowser(ctx, hostID, name)
	var lastErr error
	for i := 0; i < 25; i++ {
		if lastErr = cli.VolumeRemove(ctx, name, true); lastErr == nil {
			return
		}
		if client.IsErrNotFound(lastErr) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("cleanup: could not remove volume %q, it is left on the daemon: %v", name, lastErr)
}

// readTarEntry returns the body of the named entry in a TAR stream (the shape
// the Docker copy API hands back).
func readTarEntry(t *testing.T, r io.Reader, name string) string {
	t.Helper()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			t.Fatalf("entry %q not found in the archive", name)
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		if filepath.Base(h.Name) != name {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %q: %v", name, err)
		}
		return string(body)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func entryNames(entries []FileEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Name] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// assertVolumeLabels checks the seed labels, which is how a seeded volume is
// recognised on the Volumes page.
func assertVolumeLabels(ctx context.Context, t *testing.T, cli interface {
	VolumeInspect(context.Context, string) (volume.Volume, error)
}, name, slug, rel string) {
	t.Helper()
	v, err := cli.VolumeInspect(ctx, name)
	if err != nil {
		t.Fatalf("inspect %s: %v", name, err)
	}
	if v.Labels[seedVolLabel] != slug {
		t.Errorf("volume %s label %s = %q, want %q", name, seedVolLabel, v.Labels[seedVolLabel], slug)
	}
	if v.Labels[seedRelLabel] != rel {
		t.Errorf("volume %s label %s = %q, want %q", name, seedRelLabel, v.Labels[seedRelLabel], rel)
	}
}
