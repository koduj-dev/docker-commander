package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Deploying a project that builds its own image.
//
// `docker compose up -d` builds a service only when its image is MISSING. So the
// first deploy of a project with a `build:` section works, and every deploy after
// it silently keeps running the original image no matter what changed in the
// Dockerfile or its context — the CLI prints "Container Running" and exits 0.
// For a tool whose whole promise is "the files in the editor are what runs", that
// is a wrong answer delivered as a success.
//
// This test pins both halves: without --build a context change is ignored, with
// it the change lands. The negative half matters as much as the positive one —
// without it, a test asserting only "the new content appears" would still pass if
// `up` happened to rebuild for some unrelated reason, and would never have caught
// the bug it exists for.

// buildProject writes a minimal project that bakes marker into its image.
func buildProject(t *testing.T, dir, marker string) {
	t.Helper()
	app := filepath.Join(dir, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(app, "Dockerfile"):  "FROM " + testImage + "\nCOPY marker.txt /marker.txt\nCMD [\"sleep\", \"300\"]\n",
		filepath.Join(app, "marker.txt"):  marker,
		filepath.Join(dir, "compose.yml"): "services:\n  web:\n    build: ./app\n    command: [\"sleep\", \"300\"]\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// imageMarker reads /marker.txt out of the image compose built for the project.
func imageMarker(ctx context.Context, t *testing.T, slug string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", slug+"-web", "cat", "/marker.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the built image failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestIntegrationComposeUpRebuildsChangedContext(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}
	m, _ := newManager(t)
	ensureImage(ctx, t, m)

	const slug = "dctest-buildctx"
	dir := t.TempDir()
	buildProject(t, dir, "v1")
	t.Cleanup(func() {
		_, _ = ComposeDown(context.Background(), dir, slug, nil)
		_ = exec.Command("docker", "image", "rm", "-f", slug+"-web").Run()
	})

	// First deploy: the image doesn't exist, so it gets built either way.
	if out, err := ComposeUpFiles(ctx, dir, slug, nil, nil, nil, true); err != nil {
		t.Fatalf("first deploy failed: %v\n%s", err, out)
	}
	if got := imageMarker(ctx, t, slug); got != "v1" {
		t.Fatalf("image should carry the original context, got %q", got)
	}

	// Change the build context, then redeploy WITHOUT --build. Compose must
	// reuse the existing image — this is the behaviour that made the bug silent,
	// and pinning it is what makes the next assertion meaningful.
	buildProject(t, dir, "v2")
	if out, err := ComposeUpFiles(ctx, dir, slug, nil, nil, nil, false); err != nil {
		t.Fatalf("redeploy without build failed: %v\n%s", err, out)
	}
	if got := imageMarker(ctx, t, slug); got != "v1" {
		t.Fatalf("without --build compose should have reused the old image, but the marker is %q — "+
			"if compose now rebuilds by default, the deploy flag and its comment need revisiting", got)
	}

	// Same edit, deployed the way the app deploys: the change must land.
	if out, err := ComposeUpFiles(ctx, dir, slug, nil, nil, nil, true); err != nil {
		t.Fatalf("redeploy with build failed: %v\n%s", err, out)
	}
	if got := imageMarker(ctx, t, slug); got != "v2" {
		t.Fatalf("a deploy must rebuild an edited build context: marker is %q, expected \"v2\"", got)
	}
}
