package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/client"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// buildContextFrom returns a tar build context holding only a Dockerfile.
func buildContextFrom(t *testing.T, dockerfile string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// A build whose FROM is a private image pulls it with the credentials stored
// under Registries. Before, a build sent none, and such a FROM failed.
func TestBuildPullsAPrivateBaseImageWithStoredCredentials(t *testing.T) {
	m, ctx := newManager(t)
	if out, err := dockerCLI(ctx, nil, "image", "inspect", "registry:2", "busybox:latest"); err != nil {
		t.Skipf("needs registry:2 and busybox:latest locally: %s", out)
	}
	cph, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	m.store.SetCipher(cph)
	addr, ref, auth := privateRegistry(t, ctx)
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	const tag = "dctest-build-private-from:latest"
	t.Cleanup(func() {
		_, _ = cli.ImageRemove(context.Background(), tag, client.ImageRemoveOptions{Force: true})
	})

	var out strings.Builder
	collect := func(msg BuildMessage) { out.WriteString(msg.Stream + msg.Error) }
	dockerfile := "FROM " + ref + "\nRUN true\n"

	// Without a stored credential the private FROM can't be pulled.
	if err := m.BuildImage(ctx, 0, buildContextFrom(t, dockerfile), BuildOptions{Tags: []string{tag}}, collect); err == nil {
		t.Fatalf("the build pulled a private image with no credentials, so this test proves nothing:\n%s", out.String())
	}

	if _, err := m.store.CreateRegistry(ctx, "private", addr, auth.Username, auth.Password); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.BuildImage(ctx, 0, buildContextFrom(t, dockerfile), BuildOptions{Tags: []string{tag}}, collect); err != nil {
		t.Fatalf("build with the stored credential failed: %v\n%s", err, out.String())
	}
}

// Credentials are keyed the way the daemon looks them up: Docker Hub by its
// index URL, others by host; the oldest entry for a registry wins.
func TestBuildAuthConfigsKeys(t *testing.T) {
	got := buildAuthConfigs([]store.RegistryAuth{
		{Address: "docker.io", Username: "hub", Password: "1"},
		{Address: "ghcr.io", Username: "first", Password: "2"},
		{Address: "ghcr.io", Username: "second", Password: "3"},
	})
	if got[dockerHubConfigKey].Username != "hub" {
		t.Errorf("Docker Hub not under %s: %+v", dockerHubConfigKey, got)
	}
	if got["ghcr.io"].Username != "first" {
		t.Errorf("want the oldest ghcr.io entry, got %+v", got["ghcr.io"])
	}
}
