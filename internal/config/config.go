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
	// UpdateCheck enables the periodic GitHub-release update check that backs
	// the admin "update available" banner. On by default; set DC_UPDATE_CHECK=0
	// (or -update-check=false) to disable outbound calls on air-gapped hosts.
	UpdateCheck bool
	// MetricsToken, when set, requires a bearer token to scrape /metrics.
	// Empty means the endpoint is open (fine for loopback-only use).
	MetricsToken string

	// TLSCert/TLSKey are paths to a PEM certificate and key. When both are set,
	// the server speaks HTTPS directly (otherwise plain HTTP behind a proxy).
	TLSCert string
	TLSKey  string

	// MCPEnabled turns on the remote MCP server (and its OAuth endpoints). Off by
	// default: when false the /mcp, /oauth and MCP /.well-known routes are not
	// mounted, so a request is an unknown path (it falls through to the SPA, or a
	// 404 without an embedded UI) — no hint the feature exists. It exposes Docker
	// read/control to AI tooling over the network — enable consciously, behind
	// HTTPS. Startup logs the resolved on/off state.
	MCPEnabled bool
	// MCPPublicURL is the externally reachable base URL of this server
	// (e.g. https://docker.example.com), used as the canonical resource
	// identifier for OAuth audience binding and the protected-resource metadata.
	// Empty is fine for Bearer-only (Claude Code header) use; the OAuth flow
	// needs it set.
	MCPPublicURL string

	// Version is the build version string, set by main (not from flags/env).
	Version string
	// ConfigFile is the config file that was loaded, or "" if none.
	ConfigFile string

	// Metrics history backend. RedisAddr empty → in-memory ring buffer.
	RedisAddr        string
	RedisPassword    string
	RedisDB          int
	MetricsRetention time.Duration

	// TrustedProxies is the set of reverse-proxy networks whose forwarded client
	// IP (X-Forwarded-For) we trust. Empty (default) means forwarded headers are
	// IGNORED and the real TCP peer is used for every IP-based decision (rate
	// limits, the loopback 2FA exemption, audit) — so a remote client cannot
	// spoof its address. Set it (e.g. 127.0.0.1/32,::1/128) only for the actual
	// proxy in front of this server.
	TrustedProxies []*net.IPNet
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
		cfgPath = ""               // nothing was loaded
	}
	fileVals = vals

	def := defaultDataDir()

	var c Config
	c.ConfigFile = cfgPath // the config file actually loaded ("" if none)
	var host, addr string
	var port int
	flag.String("config", cfgPath, "path to a config file (KEY=VALUE, same keys as the environment)")
	flag.StringVar(&host, "host", envOr("DC_HOST", "127.0.0.1"), "listen host/interface (use 0.0.0.0 for all)")
	flag.IntVar(&port, "port", envInt("DC_PORT", 8470), "listen port")
	flag.IntVar(&port, "p", envInt("DC_PORT", 8470), "shorthand for -port")
	flag.StringVar(&addr, "addr", lookup("DC_ADDR"), "full listen address host:port (legacy; overrides -host/-port)")
	flag.StringVar(&c.DataDir, "data-dir", envOr("DC_DATA_DIR", def), "directory for the database and secrets")
	ttl := flag.Duration("session-ttl", 12*time.Hour, "session token lifetime")
	flag.BoolVar(&c.Dev, "dev", lookup("DC_DEV") == "1", "enable development mode (permissive CORS)")
	flag.BoolVar(&c.UpdateCheck, "update-check", lookup("DC_UPDATE_CHECK") != "0", "check GitHub for newer releases (set DC_UPDATE_CHECK=0 to disable)")
	flag.StringVar(&c.MetricsToken, "metrics-token", lookup("DC_METRICS_TOKEN"), "require this bearer token to scrape /metrics (empty = open)")
	flag.StringVar(&c.TLSCert, "tls-cert", lookup("DC_TLS_CERT"), "PEM TLS certificate path (enables HTTPS together with -tls-key)")
	flag.StringVar(&c.TLSKey, "tls-key", lookup("DC_TLS_KEY"), "PEM TLS private-key path")
	flag.BoolVar(&c.MCPEnabled, "mcp-enabled", lookup("DC_MCP_ENABLED") == "1", "enable the remote MCP server + OAuth endpoints (off by default; requires HTTPS)")
	flag.StringVar(&c.MCPPublicURL, "mcp-public-url", lookup("DC_MCP_PUBLIC_URL"), "externally reachable base URL (https://host[:port]) for MCP OAuth audience/metadata")
	flag.StringVar(&c.RedisAddr, "redis-addr", lookup("DC_REDIS_ADDR"), "Redis address (host:port) for metrics history; empty = in-memory")
	flag.StringVar(&c.RedisPassword, "redis-password", lookup("DC_REDIS_PASSWORD"), "Redis password")
	retention := flag.Duration("metrics-retention", envDuration("DC_METRICS_RETENTION", 6*time.Hour), "how long to keep metric history")
	trustedProxies := flag.String("trusted-proxies", lookup("DC_TRUSTED_PROXIES"), "comma-separated reverse-proxy IPs/CIDRs whose X-Forwarded-For is trusted (empty = trust none; use the real peer)")
	flag.Parse()

	// Parse the trusted-proxy list (IPs or CIDRs); a bad entry is a hard error
	// rather than silently falling back to trusting everything or nothing.
	nets, err := parseCIDRs(*trustedProxies)
	if err != nil {
		return c, err
	}
	c.TrustedProxies = nets

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

	// HTTPS needs both halves of the keypair.
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("both -tls-cert and -tls-key (DC_TLS_CERT/DC_TLS_KEY) must be set to enable HTTPS")
	}

	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return c, err
	}
	return c, nil
}

// parseCIDRs parses a comma-separated list of IPs and CIDRs into networks. A
// bare IP becomes a single-host network (/32 or /128). Empty entries are
// skipped; an unparseable entry is an error.
func parseCIDRs(raw string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			_, n, err := net.ParseCIDR(part)
			if err != nil {
				return nil, errors.New("invalid trusted-proxy CIDR: " + part)
			}
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(part)
		if ip == nil {
			return nil, errors.New("invalid trusted-proxy IP: " + part)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out, nil
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
