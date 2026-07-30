package docker

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestRemoteBindDeployEndToEnd is the full round trip the unit and smoke tests
// can't reach: a project on this machine deployed to a *different* Docker daemon,
// which cannot see the project folder at all.
//
// Set DC_REMOTE_DOCKER to that daemon's address to run it, e.g. a docker-in-docker
// sidecar:
//
//	docker run -d --name dind --privileged -e DOCKER_TLS_CERTDIR="" \
//	  -p 12375:2375 docker:dind --host=tcp://0.0.0.0:2375 --tls=false
//	DC_REMOTE_DOCKER=tcp://127.0.0.1:12375 go test ./internal/docker/ -run RemoteBindDeploy -v
//
// Without the seeding this deploy doesn't merely serve empty files — the remote
// daemon materialises each missing bind source as a *directory*, so a single-file
// config mount makes the container fail to start ("Is a directory").
func TestRemoteBindDeployEndToEnd(t *testing.T) {
	addr := os.Getenv("DC_REMOTE_DOCKER")
	if addr == "" {
		t.Skip("set DC_REMOTE_DOCKER=tcp://host:port (a second daemon) to run")
	}
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Registered before the teardown below so it runs *after* it (t.Cleanup is
	// LIFO): a `defer` here would close the store while the teardown still needs
	// it to resolve a Docker client, and the volumes would leak.
	t.Cleanup(func() { st.Close() })
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := st.CreateHost(ctx, &store.Host{Name: "dind", Kind: "tcp", Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st)
	t.Cleanup(m.Close)
	if _, err := m.SystemInfo(ctx, hostID); err != nil {
		t.Skipf("remote daemon %s not reachable: %v", addr, err)
	}
	h, err := st.HostByID(ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}

	// A project whose sidecar files live only on this machine: one directory
	// mount and one single-file config mount.
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/html", 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = "SHIPPED-FROM-PROJECT-FOLDER"
	writeFile(t, dir+"/html/index.html", "<h1>"+marker+"</h1>")
	writeFile(t, dir+"/custom.conf", "# "+marker)
	writeFile(t, dir+"/compose.yml", `services:
  web:
    image: nginx:alpine
    volumes:
      - ./html:/usr/share/nginx/html:ro
      - ./custom.conf:/etc/nginx/conf.d/custom.conf:ro
`)

	const slug = "dc-remote-bind-e2e"
	env, cleanupEnv, err := ComposeHostEnv(h)
	if err != nil {
		t.Fatal(err)
	}
	// Also t.Cleanup rather than defer: the teardown below runs `compose down`,
	// which still needs this env (and, for a TLS host, the certs it points at).
	t.Cleanup(cleanupEnv)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ComposeDown(bg, dir, slug, env)
		for _, rel := range []string{"html", "custom.conf"} {
			removeSeedVolume(t, m, hostID, SeedVolumeName(slug, rel))
		}
	})

	// --- the real deploy path ------------------------------------------------
	cfgJSON, err := ComposeConfigJSON(ctx, dir, slug)
	if err != nil {
		t.Fatalf("resolve compose config: %v", err)
	}
	internal, external, err := ClassifyProjectBinds(cfgJSON, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(external) != 0 {
		t.Fatalf("nothing points outside the project: %+v", external)
	}
	if len(internal) != 2 {
		t.Fatalf("expected both project binds, got %+v", internal)
	}
	if err := m.SeedProjectBinds(ctx, hostID, dir, slug, internal); err != nil {
		t.Fatalf("seeding to the remote host failed: %v", err)
	}
	ov, err := BindOverrideJSON(slug, internal)
	if err != nil {
		t.Fatal(err)
	}
	ovPath := t.TempDir() + "/override.json"
	if err := os.WriteFile(ovPath, ov, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ComposeUpFiles(ctx, dir, slug, nil, env, []string{"compose.yml", ovPath})
	if err != nil {
		t.Fatalf("remote deploy failed: %v\n%s", err, out)
	}

	// --- what actually landed on the remote host -----------------------------
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.ListContainers(ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}
	var cid string
	for _, c := range list {
		if strings.Contains(c.Name, slug) {
			cid = c.ID
			break
		}
	}
	if cid == "" {
		t.Fatalf("the deployed container isn't on the remote daemon:\n%s", out)
	}

	insp, err := cli.ContainerInspect(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	// The whole point: nothing may still be a bind mount, or the remote daemon
	// would have invented an empty path for it.
	for _, mnt := range insp.Mounts {
		if string(mnt.Type) == "bind" {
			t.Errorf("SECURITY/BUG: %s is still a bind mount on the remote host (source %q)", mnt.Destination, mnt.Source)
		}
	}
	if !insp.State.Running {
		var logs strings.Builder
		_ = m.StreamLogs(ctx, hostID, cid, false, "20", func(l LogLine) {
			logs.WriteString(l.Message + "\n")
		})
		t.Fatalf("container is not running (state %q) — a single-file mount that wasn't shipped lands as a directory:\n%s",
			insp.State.Status, logs.String())
	}

	// Read the files back out of the container: they exist only because they were
	// shipped, since the remote daemon has no access to the project folder.
	for _, path := range []string{"/usr/share/nginx/html/index.html", "/etc/nginx/conf.d/custom.conf"} {
		got, err := readFileInContainer(ctx, m, hostID, cid, path)
		if err != nil {
			t.Errorf("read %s in the remote container: %v", path, err)
			continue
		}
		if !strings.Contains(got, marker) {
			t.Errorf("%s on the remote host = %q, want it to contain %q", path, got, marker)
		}
	}
}

// readFileInContainer pulls a single file out of a container via the copy API.
func readFileInContainer(ctx context.Context, m *Manager, hostID int64, cid, path string) (string, error) {
	rc, _, err := m.CopyFrom(ctx, hostID, cid, path)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err != nil {
			return "", err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		return string(body), err
	}
}
