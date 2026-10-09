package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
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
	hosts, _, err := BuildRegistries(buildContextFrom(t, dockerfile), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.BuildImage(ctx, 0, buildContextFrom(t, dockerfile), BuildOptions{Tags: []string{tag}, Registries: hosts}, collect); err != nil {
		t.Fatalf("build with the stored credential failed: %v\n%s", err, out.String())
	}
}

// Credentials are keyed the way the daemon looks them up: Docker Hub by its
// index URL, others by host; the oldest entry for a registry wins.
func TestBuildAuthConfigsKeys(t *testing.T) {
	all := []store.RegistryAuth{
		{Address: "docker.io", Username: "hub", Password: "1"},
		{Address: "ghcr.io", Username: "first", Password: "2"},
		{Address: "ghcr.io", Username: "second", Password: "3"},
		{Address: "registry.internal:5000", Username: "other", Password: "4"},
	}
	got := buildAuthConfigs(all, []string{"docker.io", "ghcr.io"})
	if got[dockerHubConfigKey].Username != "hub" {
		t.Errorf("Docker Hub not under %s: %+v", dockerHubConfigKey, got)
	}
	if got["ghcr.io"].Username != "first" {
		t.Errorf("want the oldest ghcr.io entry, got %+v", got["ghcr.io"])
	}
	// SECURITY: the daemon gets the whole map, so a registry the Dockerfile
	// doesn't use must not be in it.
	if _, sent := got["registry.internal:5000"]; sent {
		t.Error("SECURITY: a credential for a registry the build doesn't use was sent")
	}
	if n := len(buildAuthConfigs(all, nil)); n != 0 {
		t.Errorf("no registries in use, but %d credentials were sent", n)
	}
}

// eofReader records whether its reader has been read to the end.
type eofReader struct {
	r   io.Reader
	eof bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		e.eof = true
	}
	return n, err
}

// The warning about a credential that can't be decrypted must not be the first
// thing a build says. The build context is the request body, and the first
// message sends the response headers, at which point net/http drains or cuts off
// whatever of the body is still unread. Every message must come after the daemon
// has read the whole context.
func TestBuildWarnsAboutSkippedCredentialsOnlyAfterTheContextIsRead(t *testing.T) {
	m, ctx := newManager(t)
	if out, err := dockerCLI(ctx, nil, "image", "inspect", "busybox:latest"); err != nil {
		t.Skipf("needs busybox:latest locally: %s", out)
	}
	key := make([]byte, 32)
	cph, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	m.store.SetCipher(cph)
	if _, err := m.store.CreateRegistry(ctx, "lost", "registry.invalid", "u", "p"); err != nil {
		t.Fatal(err)
	}
	key[0] = 1 // a different key: the stored secret no longer decrypts
	other, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	m.store.SetCipher(other)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	const tag = "dctest-build-warn-order:latest"
	t.Cleanup(func() {
		_, _ = cli.ImageRemove(context.Background(), tag, client.ImageRemoveOptions{Force: true})
	})

	// A context large enough that a cut-off body would show.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dockerfile := "FROM busybox:latest\nCOPY big /big\n"
	big := bytes.Repeat([]byte("x"), 1<<20)
	for _, f := range []struct {
		name string
		body []byte
	}{{"Dockerfile", []byte(dockerfile)}, {"big", big}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	body := &eofReader{r: &buf}

	var out strings.Builder
	early := false
	err = m.BuildImage(ctx, 0, body, BuildOptions{Tags: []string{tag}, Registries: []string{"registry.invalid"}}, func(msg BuildMessage) {
		if !body.eof {
			early = true
		}
		out.WriteString(msg.Stream + msg.Error)
	})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out.String())
	}
	if early {
		t.Errorf("a message was sent before the build context was read to the end:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "registry.invalid could not be decrypted") {
		t.Errorf("no warning about the skipped credential:\n%s", out.String())
	}
}
