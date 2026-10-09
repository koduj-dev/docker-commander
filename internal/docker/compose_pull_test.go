package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reconciling a mutable-tag digest drift needs Compose to actually check the
// registry: `up`'s own default pull policy is "missing" (pull only an image
// that isn't present locally at all), so a plain `up -d` silently reuses the
// stale local image and reports success without the running digest ever
// moving. ComposeUpFilesPull exists to fix exactly that.
//
// This pins both halves the same way TestIntegrationComposeUpRebuildsChangedContext
// pins --build: the negative case (no pull → no registry check) matters as
// much as the positive one, and is asserted against the same real compose
// output rather than an assumption about what the flag "should" do.

func pullTestProject(t *testing.T, dir string) {
	t.Helper()
	compose := "services:\n  web:\n    image: " + testImage + "\n    command: [\"sleep\", \"300\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationComposeUpFiles_NeverChecksTheRegistryByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}
	m, _ := newManager(t)
	ensureImage(ctx, t, m)

	const slug = "dctest-pull-off"
	dir := t.TempDir()
	pullTestProject(t, dir)
	t.Cleanup(func() { _, _ = ComposeDown(context.Background(), dir, slug, nil) })

	out, err := ComposeUpFiles(ctx, dir, slug, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("ComposeUpFiles: %v\n%s", err, out)
	}
	if strings.Contains(out, "Pulling") {
		t.Errorf("a plain deploy must not check the registry when the image is already local, got:\n%s", out)
	}
}

func TestIntegrationComposeUpFilesPull_AlwaysChecksTheRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}
	m, _ := newManager(t)
	ensureImage(ctx, t, m)

	const slug = "dctest-pull-on"
	dir := t.TempDir()
	pullTestProject(t, dir)
	t.Cleanup(func() { _, _ = ComposeDown(context.Background(), dir, slug, nil) })

	out, err := ComposeUpFilesPull(ctx, dir, slug, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("ComposeUpFilesPull: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Pulling") {
		t.Errorf("--pull always should make Compose check the registry even though the image is already local, got:\n%s", out)
	}
}
