package proxy

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"testing"
	"time"

	dockersdk "github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

const testImage = "alpine:latest"

// newDockerTestFixture mirrors internal/docker's own newManager(t) test
// helper (unexported there, so replicated here): a real docker.Manager
// against the actual local daemon, skipped cleanly under -short or when no
// daemon/image is reachable — never a hard failure for "no Docker here."
func newDockerTestFixture(t *testing.T) (*docker.Manager, *store.Store, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// A cipher is required before any registry/image-pull code path runs in
	// production (main.go sets one first) — set here too so these tests
	// exercise the same shape, not an unrealistic no-cipher store.
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(c)
	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	m := docker.NewManager(st)
	t.Cleanup(m.Close)
	if err := m.PullImage(ctx, 0, testImage, func(docker.PullProgress) {}); err != nil {
		t.Skipf("docker daemon/image not available: %v", err)
	}
	return m, st, ctx
}

// startLabeledContainer starts a real alpine container with Compose labels
// AND a published TCP port (createLabeled in internal/docker's own test
// suite doesn't publish ports — this needs one, so it's not reused). Binds
// the container's containerPort to a Docker-assigned free host port and
// returns the container id.
func startLabeledContainer(ctx context.Context, t *testing.T, m *docker.Manager, name, project, service string, containerPort int) string {
	t.Helper()
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	port, err := nat.NewPort("tcp", fmt.Sprintf("%d", containerPort))
	if err != nil {
		t.Fatal(err)
	}
	_ = cli.ContainerRemove(ctx, name, dockersdk.RemoveOptions{Force: true}) // best-effort, name may be free already
	created, err := cli.ContainerCreate(ctx,
		&dockersdk.Config{
			Image:        testImage,
			Cmd:          []string{"sleep", "300"},
			Labels:       map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service},
			ExposedPorts: nat.PortSet{port: struct{}{}},
		},
		&dockersdk.HostConfig{PortBindings: nat.PortMap{port: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "0"}}}},
		nil, nil, name)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if err := cli.ContainerStart(ctx, created.ID, dockersdk.StartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, dockersdk.RemoveOptions{Force: true})
	})
	return created.ID
}

func newLocalProject(t *testing.T, st *store.Store, ctx context.Context, slug string) int64 {
	t.Helper()
	pid, err := st.CreateProject(ctx, &store.Project{Name: slug, Slug: slug, HostID: 0, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// TestResolveBackendFindsTheRealPublishedPort is the positive case: a
// running container that's really part of the project/service, really
// publishing the mapped container port, resolves to its ACTUAL host port —
// not the container port, not a guess.
func TestResolveBackendFindsTheRealPublishedPort(t *testing.T) {
	m, st, ctx := newDockerTestFixture(t)
	const project = "dctest-proxy-backend"
	pid := newLocalProject(t, st, ctx, project)
	startLabeledContainer(ctx, t, m, "dctest-proxy-backend-web", project, "web", 8080)

	p := New(st, m, Config{CacheDir: t.TempDir()})
	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "web", TargetPort: 8080}

	b, err := p.resolveBackend(ctx, mapping)
	if err != nil {
		t.Fatalf("resolveBackend: %v", err)
	}

	// The resolved address must actually be dialable — proves it's the real
	// published host port, not the container-internal one (8080 itself is
	// never the right answer here: Docker assigned a different, dynamic
	// host port via HostPort "0").
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", b.addr)
	if err != nil {
		t.Fatalf("resolved backend %q is not dialable: %v", b.addr, err)
	}
	conn.Close()
}

func TestResolveBackendErrorsCleanlyForAStoppedContainer(t *testing.T) {
	m, st, ctx := newDockerTestFixture(t)
	const project = "dctest-proxy-stopped"
	pid := newLocalProject(t, st, ctx, project)
	id := startLabeledContainer(ctx, t, m, "dctest-proxy-stopped-web", project, "web", 8080)
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.ContainerStop(ctx, id, dockersdk.StopOptions{}); err != nil {
		t.Fatal(err)
	}

	p := New(st, m, Config{CacheDir: t.TempDir()})
	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "web", TargetPort: 8080}

	if _, err := p.resolveBackend(ctx, mapping); err == nil {
		t.Error("a stopped container must produce a clean error, not a resolved (stale) backend")
	}
}

func TestResolveBackendErrorsCleanlyForAnUnknownService(t *testing.T) {
	m, st, ctx := newDockerTestFixture(t)
	const project = "dctest-proxy-noservice"
	pid := newLocalProject(t, st, ctx, project)
	startLabeledContainer(ctx, t, m, "dctest-proxy-noservice-web", project, "web", 8080)

	p := New(st, m, Config{CacheDir: t.TempDir()})
	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "nonexistent-service", TargetPort: 8080}

	if _, err := p.resolveBackend(ctx, mapping); err == nil {
		t.Error("a service name matching no container must produce a clean error")
	}
}

func TestResolveBackendErrorsCleanlyForAnUnpublishedPort(t *testing.T) {
	m, st, ctx := newDockerTestFixture(t)
	const project = "dctest-proxy-badport"
	pid := newLocalProject(t, st, ctx, project)
	startLabeledContainer(ctx, t, m, "dctest-proxy-badport-web", project, "web", 8080)

	p := New(st, m, Config{CacheDir: t.TempDir()})
	// The container really exists and really is "web", but does not publish
	// port 9999 — only 8080 (see startLabeledContainer above).
	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "web", TargetPort: 9999}

	if _, err := p.resolveBackend(ctx, mapping); err == nil {
		t.Error("a TargetPort the container doesn't actually publish must produce a clean error, never a guessed dial target")
	}
}
