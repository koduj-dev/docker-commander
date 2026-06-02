// Package history stores a rolling window of container resource metrics for
// charting. It has two interchangeable backends selected at runtime:
//
//   - in-memory ring (default, zero-config, lost on restart)
//   - Redis (set DC_REDIS_ADDR) — survives restarts and can be shared
//
// The backend is chosen by Config; everything else in the app talks to the
// Store interface only.
package history

import (
	"context"
	"log"
	"time"
)

// Sample is one moment's resource reading for a container.
type Sample struct {
	ContainerID string
	Time        time.Time
	CPU         float64 // percent
	MemPercent  float64 // percent of limit
	MemBytes    float64 // bytes
}

// Point is a single (timestamp-ms, value) datapoint in a series.
type Point struct {
	T int64   `json:"t"` // unix millis
	V float64 `json:"v"`
}

// Metrics that can be queried.
const (
	MetricCPU      = "cpu"
	MetricMem      = "mem"      // percent
	MetricMemBytes = "membytes" // bytes
)

// Store persists and queries metric history.
type Store interface {
	Record(ctx context.Context, samples []Sample) error
	Query(ctx context.Context, containerID, metric string, since time.Time) ([]Point, error)
	Close() error
}

// Config selects and configures the backend.
type Config struct {
	RedisAddr     string // empty → in-memory
	RedisPassword string
	RedisDB       int
	Retention     time.Duration // how long to keep points
}

// Open builds the configured store. If Redis is requested but unreachable it
// logs a warning and falls back to in-memory, so the app always starts.
func Open(ctx context.Context, cfg Config) Store {
	if cfg.Retention <= 0 {
		cfg.Retention = 6 * time.Hour
	}
	if cfg.RedisAddr == "" {
		log.Printf("metrics history: in-memory store (retention %s)", cfg.Retention)
		return newMemoryStore(cfg.Retention)
	}
	rs, err := newRedisStore(ctx, cfg)
	if err != nil {
		log.Printf("metrics history: Redis at %s unavailable (%v); falling back to in-memory", cfg.RedisAddr, err)
		return newMemoryStore(cfg.Retention)
	}
	log.Printf("metrics history: Redis store at %s (retention %s)", cfg.RedisAddr, cfg.Retention)
	return rs
}

// metricValue extracts a metric's value from a sample.
func metricValue(s Sample, metric string) (float64, bool) {
	switch metric {
	case MetricCPU:
		return s.CPU, true
	case MetricMem:
		return s.MemPercent, true
	case MetricMemBytes:
		return s.MemBytes, true
	default:
		return 0, false
	}
}

// allMetrics is the set of series stored per sample.
var allMetrics = []string{MetricCPU, MetricMem, MetricMemBytes}
