// Package config loads runtime configuration from flags and environment
// variables and resolves sensible cross-platform defaults.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	// SelfUpdate allows an admin to download and apply an update from the web UI
	// (the one-tap "Update & restart"). On by default; set DC_SELF_UPDATE=0 to
	// keep the "update available" banner but forbid web-triggered self-replacement
	// of the binary (e.g. when updates are managed by your packaging/orchestrator).
	SelfUpdate bool
	// MetricsToken, when set, requires a bearer token to scrape /metrics.
	// Empty means the endpoint is open (fine for loopback-only use).
	MetricsToken string

	// TLSCert/TLSKey are paths to a PEM certificate and key. When both are set,
	// the server speaks HTTPS directly (otherwise plain HTTP behind a proxy).
	TLSCert string
	TLSKey  string

	// ACMEDomains, when non-empty, enables automatic HTTPS: a certificate is
	// obtained and renewed via ACME (Let's Encrypt by default) for these public
	// hostnames instead of a static TLSCert/TLSKey pair — mutually exclusive
	// with them. For a host sitting directly on the public internet with no
	// reverse proxy in front (the proxy case already terminates TLS itself).
	ACMEDomains []string
	// ACMEEmail is optionally registered with the ACME account, so the CA can
	// reach the operator about renewal problems or policy changes. Not required.
	ACMEEmail string
	// ACMECacheDir persists the obtained certificate/key and ACME account state
	// between restarts, so a restart doesn't re-issue a certificate and burn
	// into the CA's rate limits. Defaults to <data-dir>/acme.
	ACMECacheDir string
	// ACMEDirectoryURL overrides the ACME server (default: Let's Encrypt
	// production) — e.g. Let's Encrypt's staging directory, to test without
	// spending production rate-limit budget or minting a real cert, or an
	// entirely different ACME-compliant CA. NOT a local Pebble instance for
	// exercising this server's own obtain-and-serve path: Pebble's directory
	// endpoint uses a locally-generated, untrusted TLS cert (so this process
	// refuses to talk to it, correctly, the same as it would refuse any
	// other unverifiable server), and separately, its order-polling response
	// shape trips a gap in autocert's client library. See docs/gotchas.md.
	// Pebble is still useful for developing this package itself — see
	// internal/acme/pebble_integration_test.go, which works around both by
	// driving the ACME protocol at a lower level than this server does.
	ACMEDirectoryURL string

	// MCPEnabled turns on the remote MCP server (and its OAuth endpoints). Off by
	// default: when false the /mcp, /oauth and MCP /.well-known routes are not
	// mounted, so a request is an unknown path (it falls through to the SPA, or a
	// 404 without an embedded UI) — no hint the feature exists. It exposes Docker
	// read/control to AI tooling over the network — enable consciously, behind
	// HTTPS. Startup logs the resolved on/off state.
	MCPEnabled bool
	// MCPPublicURL is the externally reachable base URL of this server
	// (e.g. https://docker.example.com, or https://example.com/dockercmd under
	// a path), used as the canonical resource
	// identifier for OAuth audience binding and the protected-resource metadata.
	// Empty is fine for Bearer-only (Claude Code header) use; the OAuth flow
	// needs it set.
	MCPPublicURL string

	// ProxyEnabled turns on the embedded per-container reverse proxy: routing
	// public traffic for a project's domain_mappings entries to that
	// project's actual running container, over the SAME shared listener as
	// DC's own admin UI (SNI-dispatched). Off by default — this opens a
	// second public attack surface distinct from DC's own admin UI/API, so
	// it must be a conscious opt-in. Only takes effect when ACME mode is
	// also active for DC's own admin domain(s) (len(ACMEDomains) > 0); if
	// ProxyEnabled is true without ACME mode, startup logs a clear no-op
	// message rather than failing. Local-host projects only in this phase —
	// see NEXT.md's "Reverse proxy and ingress".
	ProxyEnabled bool
	// ProxyACMECacheDir persists the proxy's OWN obtained certificates and
	// ACME account state, kept in a directory separate from ACMECacheDir (DC's
	// own admin-domain cache) — a compromise of one manager's cache must not
	// expose the other's account key. Defaults to <data-dir>/proxy-acme.
	ProxyACMECacheDir string

	// Version is the build version string, set by main (not from flags/env).
	Version string
	// ConfigFile is the config file that was loaded, or "" if none.
	ConfigFile string
	// Warnings are config-file problems that don't stop the server (a key it
	// never reads, a line that isn't KEY=VALUE). main logs them at startup.
	Warnings []string

	// Metrics history backend. RedisAddr empty → in-memory ring buffer.
	RedisAddr        string
	RedisPassword    string
	RedisDB          int
	MetricsRetention time.Duration
	// MetricsInterval is how often the monitor samples every running container's
	// stats (feeding the charts/history and resource alert rules). Lower means
	// fresher data but more CPU on the app and the Docker daemon; raise it on a
	// host with many containers where the sweep is costly.
	MetricsInterval time.Duration

	// PProf exposes Go's net/http/pprof profiling endpoints under /debug/pprof.
	// Off by default; when on, the endpoints are restricted to loopback clients
	// (use an SSH tunnel) since they leak goroutine stacks and heap detail.
	PProf bool

	// LogFile, when set, is where the log goes instead of stderr, rotated at
	// 10 MiB with one older copy. For a process nothing captures stderr from:
	// a Windows Scheduled Task, or a detached run.
	LogFile string

	// DeploySilenceGrace is how long alert delivery is automatically silenced
	// for a project immediately after a successful deploy — containers
	// restarting, warming up or briefly reporting a stale health check are
	// expected noise, not an incident. 0 disables the automatic silence
	// entirely (unlike MetricsInterval, 0 here is a valid, deliberate choice,
	// not clamped to a default).
	DeploySilenceGrace time.Duration

	// TrustedProxies is the set of reverse-proxy networks whose forwarded client
	// IP (X-Forwarded-For) we trust. Empty (default) means forwarded headers are
	// IGNORED and the real TCP peer is used for every IP-based decision (rate
	// limits, the loopback 2FA exemption, audit) — so a remote client cannot
	// spoof its address. Set it (e.g. 127.0.0.1/32,::1/128) only for the actual
	// proxy in front of this server.
	TrustedProxies []*net.IPNet

	// BasePath is the URL path prefix the UI and API are published under when
	// the reverse proxy in front does not say so itself (e.g. "/dockercmd" for
	// https://example.com/dockercmd/). Normalised: leading slash, no trailing
	// one; "" means the root. Requests may arrive with or without it.
	BasePath string
	// ForwardedPrefix makes an X-Forwarded-Prefix request header override
	// BasePath for that request. On by default (DC_FORWARDED_PREFIX=0 turns it
	// off). The header is believed from any peer, but only a value made of
	// plain path segments is accepted, so it can't inject markup or redirect
	// off-site; at worst a client changes the links in its own response.
	ForwardedPrefix bool
}

// DBPath is the path to the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "docker-commander.db") }

// TLSEnabled reports whether the server should speak HTTPS — either from a
// static cert/key pair or from ACME-obtained domains. The two are mutually
// exclusive (enforced in Load), so this is never true from both at once.
func (c Config) TLSEnabled() bool {
	return (c.TLSCert != "" && c.TLSKey != "") || len(c.ACMEDomains) > 0
}

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
	settingErrs, lookedUp, flagKeys = nil, map[string]bool{}, map[string]string{}
	vals, fileWarnings, err := loadConfigFile(cfgPath)
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
	intFlag(&port, "DC_PORT", 8470, "listen port", "port", "p")
	flag.StringVar(&addr, "addr", lookup("DC_ADDR"), "full listen address host:port (legacy; overrides -host/-port)")
	flag.StringVar(&c.DataDir, "data-dir", envOr("DC_DATA_DIR", def), "directory for the database and secrets")
	ttl := flag.Duration("session-ttl", 12*time.Hour, "session token lifetime")
	boolFlag(&c.Dev, "DC_DEV", false, "enable development mode (permissive CORS)", "dev")
	boolFlag(&c.UpdateCheck, "DC_UPDATE_CHECK", true, "check GitHub for newer releases (set DC_UPDATE_CHECK=0 to disable)", "update-check")
	boolFlag(&c.SelfUpdate, "DC_SELF_UPDATE", true, "allow admins to apply updates from the web UI (set DC_SELF_UPDATE=0 to disable)", "self-update")
	flag.StringVar(&c.MetricsToken, "metrics-token", lookup("DC_METRICS_TOKEN"), "require this bearer token to scrape /metrics (empty = open)")
	flag.StringVar(&c.TLSCert, "tls-cert", lookup("DC_TLS_CERT"), "PEM TLS certificate path (enables HTTPS together with -tls-key)")
	flag.StringVar(&c.TLSKey, "tls-key", lookup("DC_TLS_KEY"), "PEM TLS private-key path")
	acmeDomains := flag.String("acme-domains", lookup("DC_ACME_DOMAINS"), "comma-separated public hostname(s): enables automatic HTTPS via ACME/Let's Encrypt (mutually exclusive with -tls-cert/-tls-key)")
	flag.StringVar(&c.ACMEEmail, "acme-email", lookup("DC_ACME_EMAIL"), "contact email registered with the ACME account (optional)")
	flag.StringVar(&c.ACMECacheDir, "acme-cache-dir", lookup("DC_ACME_CACHE_DIR"), "directory to cache the ACME certificate/account state (default: <data-dir>/acme)")
	flag.StringVar(&c.ACMEDirectoryURL, "acme-directory-url", lookup("DC_ACME_DIRECTORY_URL"), "override the ACME directory URL (default: Let's Encrypt production) — e.g. its staging directory; NOT a local Pebble instance, which this server cannot obtain a certificate through (see docs/gotchas.md)")
	boolFlag(&c.MCPEnabled, "DC_MCP_ENABLED", false, "enable the remote MCP server + OAuth endpoints (off by default; requires HTTPS)", "mcp-enabled")
	flag.StringVar(&c.MCPPublicURL, "mcp-public-url", lookup("DC_MCP_PUBLIC_URL"), "externally reachable base URL (https://host[:port][/path]) for MCP OAuth audience/metadata")
	boolFlag(&c.ProxyEnabled, "DC_PROXY_ENABLED", false, "enable the embedded per-container reverse proxy for domain_mappings (off by default; requires ACME mode for DC's own admin domain)", "proxy-enabled")
	flag.StringVar(&c.ProxyACMECacheDir, "proxy-acme-cache-dir", lookup("DC_PROXY_ACME_CACHE_DIR"), "directory to cache the proxy's own ACME certificate/account state (default: <data-dir>/proxy-acme)")
	flag.StringVar(&c.RedisAddr, "redis-addr", lookup("DC_REDIS_ADDR"), "Redis address (host:port) for metrics history; empty = in-memory")
	flag.StringVar(&c.RedisPassword, "redis-password", lookup("DC_REDIS_PASSWORD"), "Redis password")
	retention := durationFlag("DC_METRICS_RETENTION", 6*time.Hour, "how long to keep metric history", "metrics-retention")
	interval := durationFlag("DC_METRICS_INTERVAL", 15*time.Second, "how often to sample container stats (raise on hosts with many containers)", "metrics-interval")
	flag.StringVar(&c.LogFile, "log-file", lookup("DC_LOG_FILE"), "write the log to this file instead of stderr (rotated at 10 MiB, one older copy kept as <file>.1)")
	boolFlag(&c.PProf, "DC_PPROF", false, "expose net/http/pprof on a dedicated loopback listener, 127.0.0.1:6060 (for debugging)", "pprof")
	deploySilence := durationFlag("DC_DEPLOY_SILENCE_GRACE", 3*time.Minute, "automatically silence alert delivery for a project for this long after a successful deploy (0 disables)", "deploy-silence-grace")
	basePath := flag.String("base-path", lookup("DC_BASE_PATH"), "URL path prefix the UI is published under behind a reverse proxy, e.g. /dockercmd (empty = root)")
	boolFlag(&c.ForwardedPrefix, "DC_FORWARDED_PREFIX", true, "honour the X-Forwarded-Prefix request header as the URL path prefix (set DC_FORWARDED_PREFIX=0 to ignore it)", "forwarded-prefix")
	trustedProxies := flag.String("trusted-proxies", lookup("DC_TRUSTED_PROXIES"), "comma-separated reverse-proxy IPs/CIDRs whose X-Forwarded-For is trusted (empty = trust none; use the real peer)")
	flag.Parse()
	c.RedisDB = envInt("DC_REDIS_DB", 0)
	if err := effectiveSettingErrs(); err != nil {
		return c, err
	}
	if c.ACMEDirectoryURL != "" {
		if u, err := url.Parse(c.ACMEDirectoryURL); err != nil || u.Scheme != "https" || u.Host == "" {
			return c, fmt.Errorf("-acme-directory-url: %q is not an https URL with a host", c.ACMEDirectoryURL)
		}
	}

	// Parse the trusted-proxy list (IPs or CIDRs); a bad entry is a hard error
	// rather than silently falling back to trusting everything or nothing.
	nets, err := parseCIDRs(*trustedProxies)
	if err != nil {
		return c, err
	}
	c.TrustedProxies = nets

	if c.BasePath, err = NormalizePathPrefix(*basePath); err != nil {
		return c, fmt.Errorf("-base-path: %w", err)
	}
	if _, err := PublicURLPath(c.MCPPublicURL); err != nil {
		return c, fmt.Errorf("-mcp-public-url: %w", err)
	}

	// Listen address is host + port. A full -addr/DC_ADDR (legacy) still wins if
	// set, so existing host:port configs keep working.
	if addr != "" {
		c.Addr = addr
	} else {
		c.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	}

	c.MetricsRetention = *retention
	c.MetricsInterval = *interval
	// A non-positive interval is meaningless (the monitor would ignore it); clamp
	// to the default here so the resolved Config value is never misleading.
	if c.MetricsInterval <= 0 {
		c.MetricsInterval = 15 * time.Second
	}
	c.SessionTTL = *ttl
	// Unlike MetricsInterval above, 0 here is a deliberate, valid choice (the
	// flag's own help text promises "0 disables"), so it's assigned
	// unconditionally rather than clamped to a default — the default already
	// lives in the flag's own durationFlag(...) call, which *deploySilence
	// resolves to whenever nothing overrides it.
	c.DeploySilenceGrace = *deploySilence

	// HTTPS needs both halves of the keypair.
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("both -tls-cert and -tls-key (DC_TLS_CERT/DC_TLS_KEY) must be set to enable HTTPS")
	}

	c.ACMEDomains = splitCommaList(*acmeDomains)
	if len(c.ACMEDomains) > 0 {
		if c.TLSCert != "" || c.TLSKey != "" {
			return c, errors.New("-acme-domains cannot be combined with -tls-cert/-tls-key — pick one way to obtain a certificate")
		}
		for _, d := range c.ACMEDomains {
			// ACME/Let's Encrypt issues certificates for hostnames, not bare IPs —
			// catch the mistake here rather than as an opaque failure from the CA
			// the first time a certificate is actually requested.
			if net.ParseIP(d) != nil {
				return c, fmt.Errorf("-acme-domains: %q is an IP address; ACME issues certificates for hostnames only", d)
			}
		}
		if c.ACMECacheDir == "" {
			c.ACMECacheDir = filepath.Join(c.DataDir, "acme")
		}
	}
	if c.ProxyACMECacheDir == "" {
		c.ProxyACMECacheDir = filepath.Join(c.DataDir, "proxy-acme")
	}

	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return c, err
	}
	// A no-op on Unix (0o700 above already restricts it); on Windows this is
	// the actual enforcement — MkdirAll's mode bits don't translate to an NTFS
	// ACL there. Runs on every startup regardless of how dockercmd was
	// launched (foreground, the Scheduled Task installer, or the native SCM
	// service — internal/service's Windows installer applies the same
	// SecureDataDirACL at install time too, plus a stricter check a
	// pre-existing dir isn't already misconfigured), not just a freshly
	// created dir.
	if err := secureDataDir(c.DataDir); err != nil {
		return c, fmt.Errorf("restrict data dir permissions: %w", err)
	}
	c.Warnings = append(fileWarnings, unreadFileKeys(cfgPath)...)
	return c, nil
}

// settingErrs collects the settings whose value doesn't parse while Load reads
// them, by key. Load refuses to start on any of them rather than run on a
// default the admin didn't choose, unless a flag given on the command line
// overrides that setting (flags > env > file). flagKeys maps a flag to the
// setting that supplies its default. lookedUp records every key Load asks for,
// so a config file key it never reads (a typo) can be reported.
var (
	settingErrs []settingErr
	flagKeys    map[string]string
	lookedUp    map[string]bool
)

type settingErr struct {
	key string
	err error
}

// effectiveSettingErrs joins the parse errors of the settings that are in
// effect: one whose flag was given on the command line is overridden by it,
// and its bad value never applies.
func effectiveSettingErrs() error {
	overridden := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		if key, ok := flagKeys[f.Name]; ok {
			overridden[key] = true
		}
	})
	var errs []error
	for _, e := range settingErrs {
		if !overridden[e.key] {
			errs = append(errs, e.err)
		}
	}
	return errors.Join(errs...)
}

// boolFlag, intFlag and durationFlag define flags whose default comes from a
// typed setting, recording which setting each flag overrides. Every typed
// setting with a flag must go through them, or a bad env value would fail
// startup despite a valid flag.
func boolFlag(p *bool, key string, def bool, usage string, names ...string) {
	v := envBool(key, def)
	for _, n := range names {
		flag.BoolVar(p, n, v, usage)
		flagKeys[n] = key
	}
}

func intFlag(p *int, key string, def int, usage string, names ...string) {
	v := envInt(key, def)
	for _, n := range names {
		flag.IntVar(p, n, v, usage)
		flagKeys[n] = key
	}
}

func durationFlag(key string, def time.Duration, usage string, name string) *time.Duration {
	p := flag.Duration(name, envDuration(key, def), usage)
	flagKeys[name] = key
	return p
}

// unreadFileKeys names the DC_ keys in the config file that Load never asked
// for. Other keys are left alone: the file may double as a systemd
// EnvironmentFile carrying e.g. DOCKER_HOST.
func unreadFileKeys(path string) []string {
	var out []string
	for k := range fileVals {
		if strings.HasPrefix(k, "DC_") && !lookedUp[k] {
			out = append(out, fmt.Sprintf("%s: unknown setting %s is ignored", path, k))
		}
	}
	sort.Strings(out)
	return out
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

// maxPathPrefix bounds a path prefix; a real one is a segment or two.
const maxPathPrefix = 256

// NormalizePathPrefix turns a URL path prefix ("/dockercmd/", "dockercmd") into
// its canonical form ("/dockercmd"); "" and "/" mean the root and return "".
// Only plain segments of unreserved characters (RFC 3986: letters, digits and
// -._~) are accepted, and "." / ".." are not segments. The result is written
// into HTML, cookies and redirects, and this is what keeps that safe: there is
// nothing in it to escape, and it can't start with "//" (a scheme-relative URL
// pointing at another host).
func NormalizePathPrefix(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if len(p) > maxPathPrefix {
		return "", errors.New("path prefix is too long")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return "", nil
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid path prefix %q", raw)
		}
		for _, ch := range seg {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
				ch == '-' || ch == '.' || ch == '_' || ch == '~') {
				return "", fmt.Errorf("invalid path prefix %q", raw)
			}
		}
	}
	return p, nil
}

// PublicURLPath returns the path of a public base URL (DC_MCP_PUBLIC_URL) in
// the canonical form of NormalizePathPrefix: "" for https://host, "/dockercmd"
// for https://host/dockercmd/. The URL is the OAuth issuer, so it needs an
// http(s) scheme and a host: "docker.example.com" would otherwise parse as a
// path. The path becomes a route pattern, so it must be a plain prefix too; a
// query or fragment has no place in a base URL.
func PublicURLPath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("%q: want an http(s) URL with a host, e.g. https://docker.example.com", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", fmt.Errorf("%q: a base URL takes no query or fragment", raw)
	}
	return NormalizePathPrefix(u.EscapedPath())
}

// splitCommaList splits a comma-separated flag value into trimmed, non-empty
// entries.
func splitCommaList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ResolveDataDir returns the data directory for standalone CLI actions, which
// run before Load parses flags: DC_DATA_DIR if set, else the OS default. The
// `-data-dir` flag is only honoured in server mode.
func ResolveDataDir() string {
	return envOr("DC_DATA_DIR", defaultDataDir())
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
	if lookedUp != nil {
		lookedUp[key] = true
	}
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
// and a tolerated "export " prefix). An empty path yields an empty map. A line
// that isn't KEY=VALUE is skipped with a warning naming its line number.
func loadConfigFile(path string) (map[string]string, []string, error) {
	out := map[string]string{}
	if path == "" {
		return out, nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			warnings = append(warnings, fmt.Sprintf("%s:%d: not KEY=VALUE, ignored", path, i+1))
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	return out, warnings, nil
}

func envOr(key, def string) string {
	if v := lookup(key); v != "" {
		return v
	}
	return def
}

// envInt, envDuration and envBool read a typed setting; unset yields def. A
// value that doesn't parse yields def too, and is recorded in settingErrs so
// Load fails instead of running on it.
func envInt(key string, def int) int {
	v := lookup(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		settingErrs = append(settingErrs, settingErr{key, fmt.Errorf("%s=%q: not a whole number", key, v)})
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := lookup(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		settingErrs = append(settingErrs, settingErr{key, fmt.Errorf("%s=%q: not a duration, e.g. 90s, 15m or 24h (there is no unit for days)", key, v)})
		return def
	}
	return d
}

func envBool(key string, def bool) bool {
	v := lookup(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	settingErrs = append(settingErrs, settingErr{key, fmt.Errorf("%s=%q: want 1/0, true/false, yes/no or on/off", key, v)})
	return def
}
