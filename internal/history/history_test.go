package history

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreRecordAndQuery(t *testing.T) {
	ctx := context.Background()
	s := Open(ctx, Config{Retention: time.Hour}) // empty RedisAddr → in-memory
	t.Cleanup(func() { s.Close() })

	now := time.Now()
	for i := 0; i < 3; i++ {
		err := s.Record(ctx, []Sample{{
			ContainerID: "c1", Time: now.Add(time.Duration(i) * time.Second),
			CPU: float64(10 + i), MemPercent: float64(20 + i), MemBytes: float64(1000 + i),
		}})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	cpu, err := s.Query(ctx, "c1", MetricCPU, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Query cpu: %v", err)
	}
	if len(cpu) != 3 || cpu[0].V != 10 {
		t.Errorf("cpu series wrong: %+v", cpu)
	}

	mem, _ := s.Query(ctx, "c1", MetricMem, now.Add(-time.Minute))
	if len(mem) != 3 || mem[2].V != 22 {
		t.Errorf("mem series wrong: %+v", mem)
	}
	mb, _ := s.Query(ctx, "c1", MetricMemBytes, now.Add(-time.Minute))
	if len(mb) != 3 || mb[0].V != 1000 {
		t.Errorf("membytes series wrong: %+v", mb)
	}

	// Unknown container → empty series, no error.
	if pts, err := s.Query(ctx, "nope", MetricCPU, now.Add(-time.Minute)); err != nil || len(pts) != 0 {
		t.Errorf("unknown container: %v %+v", err, pts)
	}
	// since in the future → nothing.
	if pts, _ := s.Query(ctx, "c1", MetricCPU, now.Add(time.Hour)); len(pts) != 0 {
		t.Errorf("future since should yield nothing, got %d", len(pts))
	}
}

func TestOpenFallsBackToMemory(t *testing.T) {
	// An unreachable Redis must not fail Open — it falls back to in-memory.
	s := Open(context.Background(), Config{RedisAddr: "127.0.0.1:6", Retention: time.Hour})
	t.Cleanup(func() { s.Close() })
	if err := s.Record(context.Background(), []Sample{{ContainerID: "x", Time: time.Now(), CPU: 1}}); err != nil {
		t.Errorf("fallback store should accept records: %v", err)
	}
}
