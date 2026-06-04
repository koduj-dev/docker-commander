// Package config loads runtime configuration from flags and environment
// variables and resolves sensible cross-platform defaults.
package config

import (
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// Load parses flags/env/config-file and returns the resolved configuration.
//
// Precedence (highest first): command-line flag → environment variable →
// config file → built-in default. The config file is a simple "KEY=VALUE" file
// using the same DC_* keys as the environment. Its path comes from -config,
// then $DC_CONFIG, then the platform default (/etc/docker-commander/
// commander.conf on Unix, %ProgramData%\docker-commander\commander.conf on
// Windows); a missing default file is ignored, a missing explicit one errors.
func Load() (Config, error) {
	cfgPath, explicit := resolveConfigPath()
	vals, err := loadConfigFile(cfgPath)
	if err != nil {
		if explicit || !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
		vals = map[string]string{} // default file absent → ignore
	}
	fileVals = vals

	def := defaultDataDir()

	var c Config
	var host, addr string
	var port int
	flag.String("config", cfgPath, "path to a config file (KEY=VALUE, same keys as the environment)")
	flag.StringVar(&host, "host", envOr("DC_HOST", "127.0.0.1"), "listen host/interface (use 0.0.0.0 for all)")
	flag.IntVar(&port, "port", envInt("DC_PORT", 8080), "listen port")
	flag.IntVar(&port, "p", envInt("DC_PORT", 8080), "shorthand for -port")
	flag.StringVar(&addr, "addr", lookup("DC_ADDR"), "full listen address host:port (legacy; overrides -host/-port)")
	flag.StringVar(&c.DataDir, "data-dir", envOr("DC_DATA_DIR", def), "directory for the database and secrets")
	ttl := flag.Duration("session-ttl", 12*time.Hour, "session token lifetime")
	flag.BoolVar(&c.Dev, "dev", lookup("DC_DEV") == "1", "enable development mode (permissive CORS)")
	flag.StringVar(&c.MetricsToken, "metrics-token", lookup("DC_METRICS_TOKEN"), "require this bearer token to scrape /metrics (empty = open)")
	flag.StringVar(&c.RedisAddr, "redis-addr", lookup("DC_REDIS_ADDR"), "Redis address (host:port) for metrics history; empty = in-memory")
	flag.StringVar(&c.RedisPassword, "redis-password", lookup("DC_REDIS_PASSWORD"), "Redis password")
	retention := flag.Duration("metrics-retention", envDuration("DC_METRICS_RETENTION", 6*time.Hour), "how long to keep metric history")
	flag.Parse()

	// Listen address is host + port. A full -addr/DC_ADDR (legacy) still wins if
	// set, so existing host:port configs keep working.
	if addr != "" {
		c.Addr = addr
	} else {
		c.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	}

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

// fileVals holds key=value pairs parsed from the optional config file. It is
// consulted by lookup() after the environment and before built-in defaults.
var fileVals map[string]string

// lookup returns a setting from the environment, falling back to the config
// file. An empty/unset value yields "".
func lookup(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if fileVals != nil {
		return fileVals[key]
	}
	return ""
}

// resolveConfigPath picks the config-file path: -config flag, then $DC_CONFIG,
// then the platform default. `explicit` is true for the first two, so a missing
// file there is a hard error (vs. silently ignoring an absent default).
func resolveConfigPath() (path string, explicit bool) {
	if p := argConfigPath(); p != "" {
		return p, true
	}
	if p := os.Getenv("DC_CONFIG"); p != "" {
		return p, true
	}
	return defaultConfigPath(), false
}

// argConfigPath scans os.Args for -config/--config (the flag package hasn't run
// yet when we need the path, so we read it ourselves).
func argConfigPath() string {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, pre := range []string{"--config=", "-config="} {
			if v, ok := strings.CutPrefix(a, pre); ok {
				return v
			}
		}
		if (a == "-config" || a == "--config") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// defaultConfigPath is the conventional config location, or "" where there is
// no sensible default (Windows — use -config or $DC_CONFIG there).
func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		// Machine-wide config under %ProgramData% (e.g. C:\ProgramData).
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "docker-commander", "commander.conf")
		}
		return ""
	}
	return "/etc/docker-commander/commander.conf"
}

// loadConfigFile parses a "KEY=VALUE" file (with "#" comments, optional quotes
// and a tolerated "export " prefix). An empty path yields an empty map.
func loadConfigFile(path string) (map[string]string, error) {
	out := map[string]string{}
	if path == "" {
		return out, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k != "" {
			out[k] = v
		}
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := lookup(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := lookup(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := lookup(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
