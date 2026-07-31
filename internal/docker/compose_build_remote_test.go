package docker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestRemoteBuildContextDeployEndToEnd covers the combination the other tests
// each miss half of: a project that BUILDS its own image *and* bind-mounts a file
// from the project folder, deployed to a daemon that cannot see that folder.
//
// The two mechanisms are unrelated and travel differently, which is exactly why
// they're worth testing together. A build context is uploaded by the Docker build
// API as a tar from this machine — that part needs nothing from us. A bind mount
// isn't uploaded at all, so the app seeds it into a volume on the remote host and
// repoints it with a generated JSON override. Passing `-f compose.yml -f
// override.json` while also building is the real deploy invocation, and nothing
// else exercises it.
//
//	DC_REMOTE_DOCKER=tcp://127.0.0.1:12375 go test -count=1 -run RemoteBuildContext ./internal/docker/
//
// `scripts/remote-test-daemon.sh up` provisions a suitable sidecar.
func TestRemoteBuildContextDeployEndToEnd(t *testing.T) {
	addr := os.Getenv("DC_REMOTE_DOCKER")
	if addr == "" {
		t.Skip("set DC_REMOTE_DOCKER=tcp://host:port or ssh://user@host:port to run (see scripts/remote-test-daemon.sh)")
	}
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}

	kind := "tcp"
	if strings.HasPrefix(addr, "ssh://") {
		kind = "ssh"
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := st.CreateHost(ctx, &store.Host{Name: "remote-build-e2e", Kind: kind, Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st)
	t.Cleanup(m.Close)

	if kind == "ssh" {
		h, err := st.HostByID(ctx, hostID)
		if err != nil {
			t.Fatal(err)
		}
		keyLine, _, err := probeSSHHostKey(h)
		if err != nil {
			t.Skipf("cannot probe the remote host key at %s: %v", addr, err)
		}
		if err := st.SetHostKey(ctx, hostID, keyLine); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SystemInfo(ctx, hostID); err != nil {
		t.Skipf("remote daemon %s not reachable: %v", addr, err)
	}
	h, err := st.HostByID(ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}

	const slug = "dc-remote-build-e2e"
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/app", 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir+"/app/Dockerfile", "FROM "+testImage+"\nCOPY marker.txt /marker.txt\nCMD [\"sleep\", \"300\"]\n")
	writeFile(t, dir+"/app/marker.txt", "BUILT-V1")
	writeFile(t, dir+"/mounted.txt", "SEEDED-FROM-PROJECT-FOLDER")
	writeFile(t, dir+"/compose.yml", `services:
  web:
    build: ./app
    command: ["sleep", "300"]
    volumes:
      - ./mounted.txt:/mounted.txt:ro
`)

	env, cleanupEnv, err := ComposeHostEnv(h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanupEnv)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ComposeDown(bg, dir, slug, env)
		removeSeedVolume(t, m, hostID, SeedVolumeName(slug, "mounted.txt"))
	})

	deploy := func(t *testing.T) {
		t.Helper()
		cfgJSON, err := ComposeConfigJSON(ctx, dir, slug)
		if err != nil {
			t.Fatalf("resolve compose config: %v", err)
		}
		internal, external, err := ClassifyProjectBinds(cfgJSON, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(external) != 0 {
			t.Fatalf("nothing should point outside the project: %+v", external)
		}
		// The build context must NOT be mistaken for a bind mount to seed — it
		// travels with the build, and seeding it would be wasted work at best.
		if len(internal) != 1 || internal[0].Rel != "mounted.txt" {
			t.Fatalf("expected only the bind mount to need seeding, got %+v", internal)
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
		if out, err := ComposeUpFiles(ctx, dir, slug, nil, env, []string{"compose.yml", ovPath}, true); err != nil {
			t.Fatalf("remote deploy failed: %v\n%s", err, out)
		}
	}

	// readInContainer runs cat inside the deployed container on the REMOTE host,
	// so both assertions are about what actually landed there.
	readInContainer := func(t *testing.T, path string) string {
		t.Helper()
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
			t.Fatal("the deployed container isn't on the remote daemon")
		}
		out, err := readFileInContainer(ctx, m, hostID, cid, path)
		if err != nil {
			t.Fatalf("reading %s in the remote container failed: %v", path, err)
		}
		return strings.TrimSpace(out)
	}

	deploy(t)
	if got := readInContainer(t, "/marker.txt"); got != "BUILT-V1" {
		t.Errorf("the build context did not reach the remote daemon: /marker.txt = %q", got)
	}
	if got := readInContainer(t, "/mounted.txt"); got != "SEEDED-FROM-PROJECT-FOLDER" {
		t.Errorf("the seeded bind mount did not land: /mounted.txt = %q", got)
	}

	// Edit the build context and redeploy the way the app does. Both mechanisms
	// have to survive a second pass — the bind override is regenerated and the
	// image has to be rebuilt.
	writeFile(t, dir+"/app/marker.txt", "BUILT-V2")
	deploy(t)
	if got := readInContainer(t, "/marker.txt"); got != "BUILT-V2" {
		t.Errorf("a redeploy must rebuild the edited context on the remote host: /marker.txt = %q", got)
	}
	if got := readInContainer(t, "/mounted.txt"); got != "SEEDED-FROM-PROJECT-FOLDER" {
		t.Errorf("the seeded bind mount was lost on redeploy: /mounted.txt = %q", got)
	}

	t.Cleanup(func() {
		// The image is built ON the remote daemon, so it needs removing there.
		cmd := exec.Command("docker", "image", "rm", "-f", slug+"-web")
		cmd.Env = append(os.Environ(), env...)
		_ = cmd.Run()
	})
}
