package docker

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Multi-host coverage for seeded bind mounts. There is no host-to-host traffic in
// this app — every host is reached independently from here — so what's worth
// proving is that per-host state doesn't bleed: retargeting a project to another
// host, deploying to two hosts at once over *different* transports, and two
// projects on one host not clobbering each other's seeds.
//
// Provision with `scripts/remote-test-daemon.sh up 2` and use its `env` output;
// it exports host 1 over SSH and host 2 over TCP precisely so the fleet is mixed.
// Always pass -count=1 (Go caches results and the env vars don't invalidate it).

// remoteFleet resolves the configured remote hosts, skipping when too few are set.
func remoteFleet(t *testing.T, need int) (*store.Store, *Manager, []int64, []*store.Host) {
	t.Helper()
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	addrs := []string{
		os.Getenv("DC_REMOTE_DOCKER"),
		os.Getenv("DC_REMOTE_DOCKER_2"),
		os.Getenv("DC_REMOTE_DOCKER_3"),
	}
	var have []string
	for _, a := range addrs {
		if a != "" {
			have = append(have, a)
		}
	}
	if len(have) < need {
		t.Skipf("needs %d remote daemons; set DC_REMOTE_DOCKER[_2,_3] (scripts/remote-test-daemon.sh up %d)", need, need)
	}
	if !composeProbe(context.Background(), "docker") {
		t.Skip("docker compose CLI not available")
	}

	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup, not defer: teardowns registered later must still be able to
	// resolve a Docker client through this store.
	t.Cleanup(func() { st.Close() })
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	m := NewManager(st)
	t.Cleanup(m.Close)

	var ids []int64
	var hosts []*store.Host
	for i, addr := range have[:need] {
		kind := "tcp"
		if strings.HasPrefix(addr, "ssh://") {
			kind = "ssh"
		}
		id, err := st.CreateHost(ctx, &store.Host{
			Name: "fleet-" + string(rune('a'+i)), Kind: kind, Address: addr,
		})
		if err != nil {
			t.Fatal(err)
		}
		if kind == "ssh" {
			h, err := st.HostByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			keyLine, _, err := probeSSHHostKey(h)
			if err != nil {
				t.Skipf("cannot probe the host key at %s: %v", addr, err)
			}
			if err := st.SetHostKey(ctx, id, keyLine); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := m.SystemInfo(ctx, id); err != nil {
			t.Skipf("remote daemon %s not reachable: %v", addr, err)
		}
		h, err := st.HostByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("fleet host %d: %s (%s)", i+1, addr, kind)
		ids = append(ids, id)
		hosts = append(hosts, h)
	}
	return st, m, ids, hosts
}

// bindProject writes a minimal project with one directory bind and returns its
// dir plus the classified internal binds.
func bindProject(t *testing.T, marker string) (string, []ProjectBind, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/html", 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir+"/html/index.html", "<h1>"+marker+"</h1>")
	writeFile(t, dir+"/compose.yml", `services:
  web:
    image: nginx:alpine
    volumes:
      - ./html:/usr/share/nginx/html:ro
`)
	return dir, nil, marker
}

// deployTo runs the real deploy path for a project against one host.
func deployTo(t *testing.T, m *Manager, h *store.Host, hostID int64, dir, slug string) []ProjectBind {
	t.Helper()
	ctx := context.Background()
	cfgJSON, err := ComposeConfigJSON(ctx, dir, slug)
	if err != nil {
		t.Fatalf("resolve compose config: %v", err)
	}
	internal, external, err := ClassifyProjectBinds(cfgJSON, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(external) != 0 {
		t.Fatalf("unexpected external binds: %+v", external)
	}
	if err := m.SeedProjectBinds(ctx, hostID, dir, slug, internal); err != nil {
		t.Fatalf("seeding to %s failed: %v", h.Name, err)
	}
	ov, err := BindOverrideJSON(slug, internal)
	if err != nil {
		t.Fatal(err)
	}
	ovPath := t.TempDir() + "/override.json"
	if err := os.WriteFile(ovPath, ov, 0o600); err != nil {
		t.Fatal(err)
	}
	env, cleanupEnv, err := ComposeHostEnv(h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanupEnv)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ComposeDown(bg, dir, slug, env)
		for _, b := range internal {
			removeSeedVolume(t, m, hostID, SeedVolumeName(slug, b.Rel))
		}
	})
	out, err := ComposeUpFiles(ctx, dir, slug, nil, env, []string{"compose.yml", ovPath})
	if err != nil {
		t.Fatalf("deploy to %s failed: %v\n%s", h.Name, err, out)
	}
	return internal
}

// servedContent reads the seeded file out of the project's container on a host.
func servedContent(t *testing.T, m *Manager, hostID int64, slug string) string {
	t.Helper()
	ctx := context.Background()
	list, err := m.ListContainers(ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if !strings.Contains(c.Name, slug) {
			continue
		}
		body, err := readFileInContainer(ctx, m, hostID, c.ID, "/usr/share/nginx/html/index.html")
		if err != nil {
			t.Fatalf("read the served file on host %d: %v", hostID, err)
		}
		return body
	}
	return ""
}

// hasVolume reports whether a volume exists on a host.
func hasVolume(t *testing.T, m *Manager, hostID int64, name string) bool {
	t.Helper()
	cli, err := m.Client(context.Background(), hostID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.VolumeInspect(context.Background(), name); err != nil {
		return false
	}
	return true
}

// TestMultiHostRetarget covers the real "move this project to another host" flow:
// deploy to A, repoint at B, redeploy. B must end up serving the project's own
// files, and — the part that's easy to get wrong — the seeds must be created on B
// rather than only existing on A.
func TestMultiHostRetarget(t *testing.T) {
	_, m, ids, hosts := remoteFleet(t, 2)
	const slug = "dc-multihost-retarget"

	dir, _, _ := bindProject(t, "FROM-PROJECT-A")

	// Deploy to host A.
	binds := deployTo(t, m, hosts[0], ids[0], dir, slug)
	volName := SeedVolumeName(slug, binds[0].Rel)
	if got := servedContent(t, m, ids[0], slug); !strings.Contains(got, "FROM-PROJECT-A") {
		t.Fatalf("host A serves %q, want the project's file", got)
	}
	if !hasVolume(t, m, ids[0], volName) {
		t.Errorf("seed volume %q missing on host A", volName)
	}
	if hasVolume(t, m, ids[1], volName) {
		t.Errorf("seed volume %q must not exist on host B before deploying there", volName)
	}

	// Retarget: edit the files, then deploy the same project to host B.
	writeFile(t, dir+"/html/index.html", "<h1>RETARGETED-TO-B</h1>")
	deployTo(t, m, hosts[1], ids[1], dir, slug)

	if !hasVolume(t, m, ids[1], volName) {
		t.Errorf("seed volume %q was not created on host B", volName)
	}
	if got := servedContent(t, m, ids[1], slug); !strings.Contains(got, "RETARGETED-TO-B") {
		t.Errorf("host B serves %q, want the edited file — the retarget shipped stale or no content", got)
	}

	// Host A is untouched by a deploy aimed at B: its volume still holds the
	// ORIGINAL content, proving the two hosts' seeds are independent.
	if got := servedContent(t, m, ids[0], slug); !strings.Contains(got, "FROM-PROJECT-A") {
		t.Errorf("host A now serves %q — deploying to B disturbed A", got)
	}

	// Documented gap, asserted so it can't change unnoticed: retargeting does NOT
	// tear down the old host, so the project keeps running on A with its seeds.
	if !hasVolume(t, m, ids[0], volName) {
		t.Errorf("host A's seed volume vanished; retarget should leave it alone")
	}
	t.Log("NOTE: the project is still deployed on host A after retargeting to B — " +
		"retarget does not bring the old host down (tracked in NEXT.md)")
}

// TestMultiHostConcurrentMixedTransports deploys two projects to two hosts at the
// same time over different transports (SSH and TCP). Per-host state — the client
// cache and the materialised TLS/SSH environment — must not cross-contaminate, so
// each host has to end up serving its own project's content.
func TestMultiHostConcurrentMixedTransports(t *testing.T) {
	_, m, ids, hosts := remoteFleet(t, 2)

	kinds := hosts[0].Kind + "+" + hosts[1].Kind
	if hosts[0].Kind == hosts[1].Kind {
		t.Logf("both hosts use %q; the mixed-transport aspect isn't covered by this run", hosts[0].Kind)
	} else {
		t.Logf("mixed transports: %s", kinds)
	}

	type spec struct {
		slug, marker, dir string
		idx               int
	}
	specs := []spec{
		{slug: "dc-multihost-conc-a", marker: "CONCURRENT-HOST-A", idx: 0},
		{slug: "dc-multihost-conc-b", marker: "CONCURRENT-HOST-B", idx: 1},
	}
	for i := range specs {
		dir, _, _ := bindProject(t, specs[i].marker)
		specs[i].dir = dir
	}

	var wg sync.WaitGroup
	for _, s := range specs {
		wg.Add(1)
		go func(s spec) {
			defer wg.Done()
			deployTo(t, m, hosts[s.idx], ids[s.idx], s.dir, s.slug)
		}(s)
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	for _, s := range specs {
		got := servedContent(t, m, ids[s.idx], s.slug)
		if !strings.Contains(got, s.marker) {
			t.Errorf("host %d serves %q, want %q — concurrent deploys crossed over",
				s.idx+1, got, s.marker)
		}
		// The other host must not have picked up this project at all.
		other := 1 - s.idx
		if c := servedContent(t, m, ids[other], s.slug); c != "" {
			t.Errorf("project %q also landed on host %d (content %q)", s.slug, other+1, c)
		}
	}
}

// TestMultiHostTwoProjectsOneHost puts two projects with the SAME relative bind
// path on one host. The seed volume name is derived from the project slug as well
// as the path, so they must not share a volume — otherwise one project would
// serve the other's files.
func TestMultiHostTwoProjectsOneHost(t *testing.T) {
	_, m, ids, hosts := remoteFleet(t, 1)

	const slugA, slugB = "dc-sharedpath-one", "dc-sharedpath-two"
	dirA, _, _ := bindProject(t, "PROJECT-ONE")
	dirB, _, _ := bindProject(t, "PROJECT-TWO")

	bindsA := deployTo(t, m, hosts[0], ids[0], dirA, slugA)
	bindsB := deployTo(t, m, hosts[0], ids[0], dirB, slugB)

	volA := SeedVolumeName(slugA, bindsA[0].Rel)
	volB := SeedVolumeName(slugB, bindsB[0].Rel)
	if volA == volB {
		t.Fatalf("both projects mapped ./html to the same volume %q — they would overwrite each other", volA)
	}
	if !hasVolume(t, m, ids[0], volA) || !hasVolume(t, m, ids[0], volB) {
		t.Fatalf("both seed volumes should exist: %q, %q", volA, volB)
	}

	if got := servedContent(t, m, ids[0], slugA); !strings.Contains(got, "PROJECT-ONE") {
		t.Errorf("project one serves %q, want its own file", got)
	}
	if got := servedContent(t, m, ids[0], slugB); !strings.Contains(got, "PROJECT-TWO") {
		t.Errorf("project two serves %q, want its own file", got)
	}
}
