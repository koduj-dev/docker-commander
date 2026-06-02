package history

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startRedis runs a throwaway redis container and returns its host address.
// The whole test is skipped when Docker (or the image) isn't available, so it
// stays green on machines without a daemon while exercising the real Redis
// code path in CI where Docker is present.
func startRedis(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("redis integration test; skipped under -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-p", "127.0.0.1::6379", "redis:7-alpine").CombinedOutput()
	if err != nil {
		t.Skipf("cannot start redis container: %v (%s)", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	portOut, err := exec.Command("docker", "port", id, "6379/tcp").Output()
	if err != nil {
		t.Skipf("cannot read mapped port: %v", err)
	}
	// e.g. "127.0.0.1:49160" (take the first line)
	addr := strings.TrimSpace(strings.SplitN(string(portOut), "\n", 2)[0])
	if addr == "" {
		t.Skip("no mapped port")
	}
	return addr
}

func TestRedisStoreIntegration(t *testing.T) {
	addr := startRedis(t)
	ctx := context.Background()

	// Retry Open briefly while redis finishes booting.
	var s Store
	for i := 0; i < 20; i++ {
		s = Open(ctx, Config{RedisAddr: addr, Retention: time.Hour})
		if _, err := s.Query(ctx, "ping", MetricCPU, time.Unix(0, 0)); err == nil {
			break
		}
		s.Close()
		time.Sleep(250 * time.Millisecond)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.Record(ctx, []Sample{{
			ContainerID: "c1", Time: now.Add(time.Duration(i) * time.Second),
			CPU: float64(10 + i), MemPercent: float64(20 + i), MemBytes: float64(1000 + i),
		}}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	cpu, err := s.Query(ctx, "c1", MetricCPU, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(cpu) != 3 || cpu[0].V != 10 {
		t.Errorf("redis cpu series wrong: %+v", cpu)
	}
	// since in the future → empty.
	if pts, _ := s.Query(ctx, "c1", MetricCPU, now.Add(time.Hour)); len(pts) != 0 {
		t.Errorf("future since should be empty, got %d", len(pts))
	}
}
