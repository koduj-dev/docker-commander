// Package config loads runtime configuration from flags and environment
// variables and resolves sensible cross-platform defaults.
package config

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds all runtime options.
type Config struct {
	// Addr is the listen address. Defaults to loopback only; binding to a
	// public interface is an explicit, conscious choice by the operator.
	Addr string
	// DataDir holds the SQLite database and any persisted secrets.
	DataDir string
	// SessionTTL is how long a logged-in session token stays valid.
	SessionTTL time.Duration
	// Dev enables developer conveniences (e.g. permissive CORS for Vite).
	Dev bool
	// MetricsToken, when set, requires a bearer token to scrape /metrics.
	// Empty means the endpoint is open (fine for loopback-only use).
	MetricsToken string

	// Metrics history backend. RedisAddr empty → in-memory ring buffer.
	RedisAddr        string
	RedisPassword    string
	RedisDB          int
	MetricsRetention time.Duration
}

// DBPath is the path to the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "docker-commander.db") }

// Load parses flags/env and returns the resolved configuration.
func Load() (Config, error) {
	def := defaultDataDir()

	var c Config
	flag.StringVar(&c.Addr, "addr", envOr("DC_ADDR", "127.0.0.1:8080"), "listen address (host:port)")
	flag.StringVar(&c.DataDir, "data-dir", envOr("DC_DATA_DIR", def), "directory for the database and secrets")
	ttl := flag.Duration("session-ttl", 12*time.Hour, "session token lifetime")
	flag.BoolVar(&c.Dev, "dev", os.Getenv("DC_DEV") == "1", "enable development mode (permissive CORS)")
	flag.StringVar(&c.MetricsToken, "metrics-token", os.Getenv("DC_METRICS_TOKEN"), "require this bearer token to scrape /metrics (empty = open)")
	flag.StringVar(&c.RedisAddr, "redis-addr", os.Getenv("DC_REDIS_ADDR"), "Redis address (host:port) for metrics history; empty = in-memory")
	flag.StringVar(&c.RedisPassword, "redis-password", os.Getenv("DC_REDIS_PASSWORD"), "Redis password")
	retention := flag.Duration("metrics-retention", envDuration("DC_METRICS_RETENTION", 6*time.Hour), "how long to keep metric history")
	flag.Parse()

	c.RedisDB = envInt("DC_REDIS_DB", 0)
	c.MetricsRetention = *retention
	c.SessionTTL = *ttl
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return c, err
	}
	return c, nil
}

// defaultDataDir returns an OS-appropriate per-user config directory.
func defaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "docker-commander")
	}
	return ".docker-commander"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
