package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// A restore that reseeds a remote project's volumes and then fails must put the
// volumes back as they were: the seeded files, what the containers wrote there
// since (owner included), and no volume the restore created. Drives the
// snapshot against a real daemon; only this test's volumes are touched.
func TestSeedSnapshotRestoresVolumes_Integration(t *testing.T) {
	m, ctx := newManager(t)
	const slug = "seed-snapshot-integration"
	html := ProjectBind{Service: "web", Target: "/usr/share/nginx/html", Rel: "html"}
	assets := ProjectBind{Service: "web", Target: "/assets", Rel: "assets"}
	t.Cleanup(func() {
		for _, b := range []ProjectBind{html, assets} {
			removeSeedVolume(t, m, 0, SeedVolumeName(slug, b.Rel))
		}
	})

	v1 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(v1, "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(v1, "html", "index.html"), "v1")
	writeFile(t, filepath.Join(v1, "html", "old.txt"), "only in v1")
	if err := m.SeedProjectBinds(ctx, 0, v1, slug, []ProjectBind{html}); err != nil {
		t.Fatal(err)
	}
	htmlVol := SeedVolumeName(slug, "html")
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	inVolume := func(cmd string) string {
		t.Helper()
		id, err := m.volumeHelper(ctx, 0, htmlVol)
		if err != nil {
			t.Fatal(err)
		}
		out, stderr, code, err := execCapture(ctx, cli, id, []string{"sh", "-c", cmd})
		if err != nil || code != 0 {
			t.Fatalf("%s: %v %s", cmd, err, stderr)
		}
		return strings.TrimSpace(out)
	}
	// What a running container wrote into the volume since the last deploy.
	inVolume("echo runtime > /data/runtime.txt && chown 1000:1000 /data/runtime.txt")

	snap, err := m.SnapshotSeedVolumes(ctx, 0, slug, []ProjectBind{html, assets})
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Discard(ctx)

	// The restore's seed: other files, and a volume that didn't exist.
	v2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(v2, "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(v2, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(v2, "html", "index.html"), "v2")
	writeFile(t, filepath.Join(v2, "html", "new.txt"), "only in v2")
	writeFile(t, filepath.Join(v2, "assets", "a.css"), "x")
	if err := m.SeedProjectBinds(ctx, 0, v2, slug, []ProjectBind{html, assets}); err != nil {
		t.Fatal(err)
	}
	if got := inVolume("cat /data/index.html"); got != "v2" {
		t.Fatalf("the reseed didn't take, so this test proves nothing: %q", got)
	}

	if err := snap.Restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if got := inVolume("cat /data/index.html"); got != "v1" {
		t.Errorf("index.html = %q, want v1 back", got)
	}
	if got := inVolume("ls -A /data | sort | tr '\\n' ' '"); got != "index.html old.txt runtime.txt" {
		t.Errorf("volume holds %q, want exactly what was there before the reseed", got)
	}
	if got := inVolume("stat -c %u /data/runtime.txt"); got != "1000" {
		t.Errorf("runtime.txt is owned by uid %s, want 1000 as the container left it", got)
	}
	if _, err := cli.VolumeInspect(ctx, SeedVolumeName(slug, "assets"), client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("the volume the reseed created should be gone, inspect err = %v", err)
	}
}
