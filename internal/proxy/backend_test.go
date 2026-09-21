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
// suite doesn't publish ports — this needs one, so it's not reused), bound
// to hostIP (use "0.0.0.0" for the ordinary wildcard case most tests want).
// Binds the container's containerPort to a Docker-assigned free host port
// and returns the container id.
func startLabeledContainer(ctx context.Context, t *testing.T, m *docker.Manager, name, project, service string, containerPort int, hostIP string) string {
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
			Image: testImage,
			// A real listener on the mapped port (busybox httpd applet), so a dial to the
			// published port reaches something. `sleep` would leave the port
			// answered only by Docker's userland proxy — and refused outright on
			// a daemon running without one.
			Cmd:          []string{"busybox", "httpd", "-f", "-p", fmt.Sprintf("%d", containerPort), "-h", "/tmp"},
			Labels:       map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service},
			ExposedPorts: nat.PortSet{port: struct{}{}},
		},
		&dockersdk.HostConfig{PortBindings: nat.PortMap{port: []nat.PortBinding{{HostIP: hostIP, HostPort: "0"}}}},
		nil, nil, name)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	// Registered BEFORE the start: a container that was created but failed to
	// start must still be removed, or it lingers and poisons later runs.
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, dockersdk.RemoveOptions{Force: true})
	})
	if err := cli.ContainerStart(ctx, created.ID, dockersdk.StartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return created.ID
}

// dialEventually dials addr until it connects or the deadline passes: the
// container's listener starts a moment after ContainerStart returns, and on a
// daemon without a userland proxy a dial before that is refused.
func dialEventually(ctx context.Context, t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resolved backend %q is not dialable: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
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
	startLabeledContainer(ctx, t, m, "dctest-proxy-backend-web", project, "web", 8080, "0.0.0.0")

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
	dialEventually(ctx, t, b.addr)
}

func TestResolveBackendErrorsCleanlyForAStoppedContainer(t *testing.T) {
	m, st, ctx := newDockerTestFixture(t)
	const project = "dctest-proxy-stopped"
	pid := newLocalProject(t, st, ctx, project)
	id := startLabeledContainer(ctx, t, m, "dctest-proxy-stopped-web", project, "web", 8080, "0.0.0.0")
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
	startLabeledContainer(ctx, t, m, "dctest-proxy-noservice-web", project, "web", 8080, "0.0.0.0")

	p := New(st, m, Config{CacheDir: t.TempDir()})
	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "nonexistent-service", TargetPort: 8080}

	if _, err := p.resolveBackend(ctx, mapping); err == nil {
		t.Error("a service name matching no container must produce a clean error")
	}
}

// TestBindDialAddr is the regression for a P1 a code review caught before
// merge: the resolver used to hardcode "127.0.0.1" regardless of what IP
// Docker actually reported the port bound to. A container can legitimately
// publish to a specific, non-wildcard address while an entirely unrelated
// process owns 127.0.0.1 on that same port number — those two bindings
// coexist without conflict — so assuming loopback would proxy public
// traffic to whatever unrelated thing happens to be listening there
// instead of the container the mapping is actually about.
func TestBindDialAddr(t *testing.T) {
	cases := []struct{ ip, want string }{
		{"", "127.0.0.1:8080"},
		{"0.0.0.0", "127.0.0.1:8080"},
		{"::", "[::1]:8080"},
		{"192.0.2.10", "192.0.2.10:8080"}, // a SPECIFIC bind must be used exactly as reported, never coerced to loopback
		{"127.0.0.2", "127.0.0.2:8080"},   // including a specific loopback alias, not just the "usual" 127.0.0.1
	}
	for _, c := range cases {
		if got := bindDialAddr(c.ip, 8080); got != c.want {
			t.Errorf("bindDialAddr(%q, 8080) = %q, want %q", c.ip, got, c.want)
		}
	}
}

// TestResolveBackendUsesTheExactPublishedBindIP: a container published to a
// specific address (127.0.0.2, not the default wildcard 0.0.0.0) must
// resolve to THAT exact address — proving the fix dials Docker's reported
// binding rather than a hardcoded loopback guess. 127.0.0.0/8 is all
// loopback on Linux, so 127.0.0.2 is real and dialable in any sandbox
// without needing an actual non-loopback interface.
func TestResolveBackendUsesTheExactPublishedBindIP(t *testing.T) {
	m, st, ctx := newDockerTestFixture(t)
	const project = "dctest-proxy-specific-ip"
	pid := newLocalProject(t, st, ctx, project)
	startLabeledContainer(ctx, t, m, "dctest-proxy-specific-ip-web", project, "web", 8080, "127.0.0.2")

	p := New(st, m, Config{CacheDir: t.TempDir()})
	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "web", TargetPort: 8080}

	b, err := p.resolveBackend(ctx, mapping)
	if err != nil {
		t.Fatalf("resolveBackend: %v", err)
	}
	host, _, err := net.SplitHostPort(b.addr)
	if err != nil {
		t.Fatalf("resolved addr %q: %v", b.addr, err)
	}
	if host != "127.0.0.2" {
		t.Errorf("resolved host = %q, want the exact published bind IP 127.0.0.2 (not a hardcoded 127.0.0.1)", host)
	}
	dialEventually(ctx, t, b.addr)
}

func TestResolveBackendErrorsCleanlyForAnUnpublishedPort(t *testing.T) {
	m, st, ctx := newDockerTestFixture(t)
	const project = "dctest-proxy-badport"
	pid := newLocalProject(t, st, ctx, project)
	startLabeledContainer(ctx, t, m, "dctest-proxy-badport-web", project, "web", 8080, "0.0.0.0")

	p := New(st, m, Config{CacheDir: t.TempDir()})
	// The container really exists and really is "web", but does not publish
	// port 9999 — only 8080 (see startLabeledContainer above).
	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "web", TargetPort: 9999}

	if _, err := p.resolveBackend(ctx, mapping); err == nil {
		t.Error("a TargetPort the container doesn't actually publish must produce a clean error, never a guessed dial target")
	}
}
