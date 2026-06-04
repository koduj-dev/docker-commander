package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// newManager builds a Manager backed by an in-memory store with a local host,
// skipping the test if no Docker daemon is reachable.
func newManager(t *testing.T) (*Manager, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := NewManager(st)
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.SystemInfo(ctx, 0); err != nil {
		t.Skipf("docker daemon not available: %v", err)
	}
	return m, ctx
}

const testImage = "alpine:latest"

// ensureImage makes sure alpine is present (pulling it exercises PullImage).
func ensureImage(t *testing.T, m *Manager, ctx context.Context) {
	t.Helper()
	imgs, _ := m.ListImages(ctx, 0)
	for _, im := range imgs {
		for _, tag := range im.RepoTags {
			if tag == testImage {
				return
			}
		}
	}
	if err := m.PullImage(ctx, 0, testImage, func(PullProgress) {}); err != nil {
		t.Skipf("cannot pull %s: %v", testImage, err)
	}
}

// rawClient returns the underlying client for cleanup ops the Manager doesn't expose.
func rmContainer(t *testing.T, m *Manager, ctx context.Context, id string) {
	cli, err := m.Client(ctx, 0)
	if err != nil {
		return
	}
	_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

func startTestContainer(t *testing.T, m *Manager, ctx context.Context, name string) string {
	t.Helper()
	ensureImage(t, m, ctx)
	id, err := m.CreateContainer(ctx, 0, CreateSpec{
		Image: testImage, Name: name, Cmd: []string{"sleep", "300"},
		Env: []string{"FOO=bar"}, Start: true,
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rmContainer(t, m, ctx, id) })
	return id
}

func TestIntegrationSystemAndLists(t *testing.T) {
	m, ctx := newManager(t)

	if info, err := m.SystemInfo(ctx, 0); err != nil || info.ServerVersion == "" {
		t.Errorf("SystemInfo: %+v err=%v", info, err)
	} else if info.OSType == "" || info.KernelVersion == "" || info.StorageDriver == "" {
		t.Errorf("SystemInfo host detail fields should be populated: %+v", info)
	}
	if _, err := m.DiskUsage(ctx, 0); err != nil {
		t.Errorf("DiskUsage: %v", err)
	}
	if _, err := m.ListContainers(ctx, 0); err != nil {
		t.Errorf("ListContainers: %v", err)
	}
	if _, err := m.ListImages(ctx, 0); err != nil {
		t.Errorf("ListImages: %v", err)
	}
	if _, err := m.ListNetworks(ctx, 0); err != nil {
		t.Errorf("ListNetworks: %v", err)
	}
	if _, err := m.Topology(ctx, 0); err != nil {
		t.Errorf("Topology: %v", err)
	}
}

func TestIntegrationContainerLifecycle(t *testing.T) {
	m, ctx := newManager(t)
	id := startTestContainer(t, m, ctx, "dctest_life")

	d, err := m.InspectContainer(ctx, 0, id)
	if err != nil || d.State != "running" {
		t.Fatalf("InspectContainer: %+v err=%v", d, err)
	}
	if _, err := m.SampleStats(ctx, 0, id); err != nil {
		t.Errorf("SampleStats: %v", err)
	}
	if _, err := m.ContainerDiff(ctx, 0, id); err != nil {
		t.Errorf("ContainerDiff: %v", err)
	}
	if top, err := m.ContainerTop(ctx, 0, id); err != nil || len(top.Titles) == 0 {
		t.Errorf("ContainerTop: %+v err=%v", top, err)
	}
	if _, err := m.InspectRaw(ctx, 0, "container", id); err != nil {
		t.Errorf("InspectRaw container: %v", err)
	}

	// files: list /, upload to /tmp, read back, delete
	if entries, err := m.ListPath(ctx, 0, id, "/"); err != nil || len(entries) == 0 {
		t.Errorf("ListPath /: %d err=%v", len(entries), err)
	}
	tarBuf := makeTar(t, "hello.txt", []byte("hi"))
	if err := m.CopyTo(ctx, 0, id, "/tmp", tarBuf); err != nil {
		t.Errorf("CopyTo: %v", err)
	}
	rc, _, err := m.CopyFrom(ctx, 0, id, "/tmp/hello.txt")
	if err != nil {
		t.Errorf("CopyFrom: %v", err)
	} else {
		rc.Close()
	}
	if err := m.DeletePath(ctx, 0, id, "/tmp/hello.txt"); err != nil {
		t.Errorf("DeletePath: %v", err)
	}

	// rename + update limits + commit
	if err := m.RenameContainer(ctx, 0, id, "dctest_life2"); err != nil {
		t.Errorf("RenameContainer: %v", err)
	}
	if err := m.UpdateContainer(ctx, 0, id, 64*1024*1024, 0, "on-failure"); err != nil {
		t.Errorf("UpdateContainer: %v", err)
	}
	imgID, err := m.CommitContainer(ctx, 0, id, "dctest-commit:1", "snapshot")
	if err != nil || imgID == "" {
		t.Errorf("CommitContainer: %q err=%v", imgID, err)
	} else {
		t.Cleanup(func() { _, _ = m.RemoveImage(ctx, 0, "dctest-commit:1", true) })
	}

	// export filesystem
	if rc, err := m.ExportContainer(ctx, 0, id); err != nil {
		t.Errorf("ExportContainer: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, rc)
		rc.Close()
	}

	// lifecycle actions
	for _, a := range []string{"pause", "unpause", "restart", "stop", "start", "kill"} {
		if err := m.ContainerAction(ctx, 0, id, a); err != nil {
			t.Errorf("ContainerAction %s: %v", a, err)
		}
	}
	if err := m.ContainerAction(ctx, 0, id, "bogus"); err != ErrUnknownAction {
		t.Errorf("unknown action should be ErrUnknownAction, got %v", err)
	}
}

func TestIntegrationImagesTransfer(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(t, m, ctx)

	if err := m.TagImage(ctx, 0, testImage, "dctest-xfer:1"); err != nil {
		t.Fatalf("TagImage: %v", err)
	}
	// history + inspect
	if h, err := m.ImageHistory(ctx, 0, "dctest-xfer:1"); err != nil || len(h) == 0 {
		t.Errorf("ImageHistory: %d err=%v", len(h), err)
	}
	if _, err := m.InspectRaw(ctx, 0, "image", "dctest-xfer:1"); err != nil {
		t.Errorf("InspectRaw image: %v", err)
	}

	// save → remove → load round trip
	rc, err := m.SaveImage(ctx, 0, []string{"dctest-xfer:1"})
	if err != nil {
		t.Fatalf("SaveImage: %v", err)
	}
	var tar bytes.Buffer
	_, _ = io.Copy(&tar, rc)
	rc.Close()
	if tar.Len() == 0 {
		t.Fatal("SaveImage produced empty tar")
	}
	if _, err := m.RemoveImage(ctx, 0, "dctest-xfer:1", true); err != nil {
		t.Errorf("RemoveImage: %v", err)
	}
	if out, err := m.LoadImage(ctx, 0, &tar); err != nil || !strings.Contains(out, "dctest-xfer") {
		t.Errorf("LoadImage: %q err=%v", out, err)
	}
	t.Cleanup(func() { _, _ = m.RemoveImage(ctx, 0, "dctest-xfer:1", true) })

	if _, err := m.PruneImages(ctx, 0); err != nil {
		t.Errorf("PruneImages: %v", err)
	}
}

func TestIntegrationVolumes(t *testing.T) {
	m, ctx := newManager(t)
	name := "dctest_vol"
	if _, err := m.CreateVolume(ctx, 0, name, "local", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	t.Cleanup(func() { _ = m.RemoveVolume(ctx, 0, name, true) })

	vols, err := m.ListVolumes(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vols {
		if v.Name == name {
			found = true
		}
	}
	if !found {
		t.Error("created volume not listed")
	}
	if _, err := m.InspectRaw(ctx, 0, "volume", name); err != nil {
		t.Errorf("InspectRaw volume: %v", err)
	}
	if err := m.RemoveVolume(ctx, 0, name, false); err != nil {
		t.Errorf("RemoveVolume: %v", err)
	}
	if _, err := m.PruneVolumes(ctx, 0); err != nil {
		t.Errorf("PruneVolumes: %v", err)
	}
}

func TestIntegrationNetworkRemove(t *testing.T) {
	m, ctx := newManager(t)
	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.NetworkCreate(ctx, "dctest_net", network.CreateOptions{})
	if err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	t.Cleanup(func() { _ = cli.NetworkRemove(ctx, resp.ID) })
	if err := m.RemoveNetwork(ctx, 0, resp.ID); err != nil {
		t.Errorf("RemoveNetwork: %v", err)
	}
	// removing a predefined network is refused
	if err := m.RemoveNetwork(ctx, 0, "bridge"); err == nil {
		t.Error("removing 'bridge' should fail")
	}
}

func TestIntegrationLogsAndEvents(t *testing.T) {
	m, ctx := newManager(t)
	id := startTestContainer(t, m, ctx, "dctest_logs")

	// StreamLogs: tail a bit; bounded by a short context.
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = m.StreamLogs(lctx, 0, id, true, "10", func(LogLine) {})

	// StreamEvents: trigger an action and expect at least one event. The trigger
	// runs in a goroutine (and uses the instant `kill`, not a stop that waits) so
	// the watch window isn't consumed by a blocking action call.
	ectx, ecancel := context.WithTimeout(ctx, 6*time.Second)
	defer ecancel()
	got := make(chan struct{}, 1)
	go func() {
		_ = m.StreamEvents(ectx, 0, func(EventMsg) {
			select {
			case got <- struct{}{}:
			default:
			}
		})
	}()
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = m.ContainerAction(ctx, 0, id, "kill")
	}()
	select {
	case <-got:
	case <-ectx.Done():
		t.Error("expected at least one docker event")
	}
}

func TestIntegrationProbePorts(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(t, m, ctx) // alpine is already present — no extra pull
	id, err := m.CreateContainer(ctx, 0, CreateSpec{
		Image: testImage, Name: "dctest_probe", Cmd: []string{"sleep", "300"}, Start: true,
		Ports: []PortSpec{{ContainerPort: "80", HostPort: "0"}}, // published; nothing listens
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rmContainer(t, m, ctx, id) })

	// Host-wide scan: the published port appears (closed, since nothing listens).
	host, err := m.ProbeHostPorts(ctx, 0)
	if err != nil {
		t.Fatalf("ProbeHostPorts: %v", err)
	}
	var found bool
	for _, p := range host {
		if p.ContainerID == id && p.PublicPort != 0 {
			found = true
			if p.GuessByPort != "HTTP" { // container port 80
				t.Errorf("passive guess for :80 should be HTTP: %+v", p)
			}
		}
	}
	if !found {
		t.Errorf("host scan should include the published port: %+v", host)
	}

	// Per-container probe returns the same port.
	if c, err := m.ProbeContainerPorts(ctx, 0, id); err != nil || len(c) == 0 {
		t.Errorf("ProbeContainerPorts: %v %+v", err, c)
	}
}

func TestIntegrationResourceOverview(t *testing.T) {
	m, ctx := newManager(t)
	id := startTestContainer(t, m, ctx, "dctest_overview")

	ov, err := m.ResourceOverview(ctx, 0)
	if err != nil {
		t.Fatalf("ResourceOverview: %v", err)
	}
	if ov.CPUs <= 0 || ov.MemTotal <= 0 {
		t.Errorf("host totals should be populated: cpus=%d mem=%d", ov.CPUs, ov.MemTotal)
	}
	var found bool
	for _, c := range ov.Containers {
		if c.ID == id {
			found = true
			if c.CPUPercent < 0 || c.CPUPercent > 100 || c.MemPercent < 0 || c.MemPercent > 100 {
				t.Errorf("shares must be 0..100%%: %+v", c)
			}
		}
	}
	if !found {
		t.Errorf("the running container %s should appear in the overview", id[:12])
	}
}

func TestIntegrationExecAndStats(t *testing.T) {
	m, ctx := newManager(t)
	id := startTestContainer(t, m, ctx, "dctest_exec")

	// Exec a command over a TTY, write to stdin, read merged output.
	sess, err := m.ExecAttach(ctx, 0, id, []string{"sh", "-c", "echo hello-exec"}, 80, 24)
	if err != nil {
		t.Fatalf("ExecAttach: %v", err)
	}
	if sess.Conn() == nil {
		t.Error("Conn() should expose the hijacked connection")
	}
	_ = sess.Resize(ctx, 100, 30)
	_, _ = sess.Write([]byte("\n"))
	_ = sess.Conn().SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, _ := sess.Read(buf)
	if !strings.Contains(string(buf[:n]), "hello-exec") {
		t.Errorf("exec output missing, got %q", buf[:n])
	}
	sess.Close()

	// Stream one stats sample, then cancel.
	sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	got := make(chan struct{}, 1)
	_ = m.StreamStats(sctx, 0, id, func(StatsSample) {
		select {
		case got <- struct{}{}:
			cancel()
		default:
		}
	})
	select {
	case <-got:
	default:
		t.Error("expected at least one stats sample")
	}
}

func TestIntegrationBuildImage(t *testing.T) {
	m, ctx := newManager(t)
	ensureImage(t, m, ctx)

	// Minimal build context: a Dockerfile that just adds a label (no RUN, so it
	// needs nothing beyond the base image already pulled by ensureImage).
	dockerfile := "FROM " + testImage + "\nLABEL dctest=1\n"
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(dockerfile))
	tw.Close()

	var sawMsg bool
	err := m.BuildImage(ctx, 0, &buf, BuildOptions{Tags: []string{"dctest/built:latest"}}, func(BuildMessage) { sawMsg = true })
	if err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
	if !sawMsg {
		t.Error("expected at least one build message")
	}
	t.Cleanup(func() { _, _ = m.RemoveImage(ctx, 0, "dctest/built:latest", true) })
}

func TestIntegrationImportAndRegistryLogin(t *testing.T) {
	m, ctx := newManager(t)
	id := startTestContainer(t, m, ctx, "dctest_import")

	// Export the container's filesystem and re-import it as a fresh image.
	rc, err := m.ExportContainer(ctx, 0, id)
	if err != nil {
		t.Fatalf("ExportContainer: %v", err)
	}
	defer rc.Close()
	imgID, err := m.ImportImage(ctx, 0, rc, "dctest/imported:latest")
	if err != nil {
		t.Fatalf("ImportImage: %v", err)
	}
	if imgID == "" {
		t.Error("ImportImage returned empty id")
	}
	t.Cleanup(func() { _, _ = m.RemoveImage(ctx, 0, "dctest/imported:latest", true) })

	// RegistryLogin with bogus credentials must fail (covers the auth path
	// without needing a real registry).
	err = m.RegistryLogin(ctx, 0, store.RegistryAuth{Address: "registry.invalid.example", Username: "u", Password: "p"})
	if err == nil {
		t.Error("login to a bogus registry should fail")
	}
}

// makeTar builds a one-file tar archive for CopyTo.
func makeTar(t *testing.T, name string, data []byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	return &buf
}
