package config

import (
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	// Load() uses the global flag set + os.Args; isolate both so the test
	// binary's own flags don't interfere.
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	dir := t.TempDir()
	t.Setenv("DC_ADDR", "0.0.0.0:9999")
	t.Setenv("DC_DATA_DIR", dir)
	t.Setenv("DC_REDIS_DB", "2")
	t.Setenv("DC_METRICS_RETENTION", "45m")
	t.Setenv("DC_METRICS_INTERVAL", "20s")
	t.Setenv("DC_PPROF", "1")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "0.0.0.0:9999" || c.DataDir != dir || c.RedisDB != 2 || c.MetricsRetention != 45*time.Minute {
		t.Errorf("Load mapped env wrong: %+v", c)
	}
	if c.MetricsInterval != 20*time.Second {
		t.Errorf("DC_METRICS_INTERVAL not mapped: %v", c.MetricsInterval)
	}
	if !c.PProf {
		t.Errorf("DC_PPROF=1 should enable pprof, got %v", c.PProf)
	}
	if c.SessionTTL != 12*time.Hour {
		t.Errorf("default session TTL: %v", c.SessionTTL)
	}
	if len(c.TrustedProxies) != 0 {
		t.Errorf("trusted proxies should be empty by default, got %v", c.TrustedProxies)
	}
}

// TestLoadCallsSecureDataDir proves Load's wiring: it actually invokes
// secureDataDir (not just MkdirAll) with the resolved data dir, on every
// call — not just a fresh directory. The real Windows ACL implementation is
// only exercised on windows-latest CI (see
// .github/workflows/windows-service-smoke.yml); this proves the
// platform-independent part, that Load reaches the hook at all, on whatever
// platform the test suite runs on.
func TestLoadCallsSecureDataDir(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	oldSecure := secureDataDir
	defer func() { secureDataDir = oldSecure }()
	var gotDir string
	secureDataDir = func(dir string) error {
		gotDir = dir
		return nil
	}

	dir := t.TempDir()
	t.Setenv("DC_DATA_DIR", dir)
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotDir != dir {
		t.Errorf("secureDataDir called with %q, want the resolved data dir %q", gotDir, dir)
	}
}

// TestLoadPropagatesSecureDataDirFailure is the DC-SEC-001 regression: a
// directory that can't be secured must fail Load outright, not start the
// server against a data dir whose permissions couldn't be confirmed.
func TestLoadPropagatesSecureDataDirFailure(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	oldSecure := secureDataDir
	defer func() { secureDataDir = oldSecure }()
	secureDataDir = func(dir string) error { return errors.New("simulated ACL failure") }

	t.Setenv("DC_DATA_DIR", t.TempDir())
	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error, want it to propagate a secureDataDir failure rather than start anyway")
	}
}

// TestLoadMetricsIntervalDefault checks the stats interval defaults to 15s when
// nothing overrides it, and that pprof is off by default.
func TestLoadMetricsIntervalDefault(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}
	t.Setenv("DC_DATA_DIR", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MetricsInterval != 15*time.Second {
		t.Errorf("default metrics interval = %v, want 15s", c.MetricsInterval)
	}
	if c.PProf {
		t.Error("pprof must be off by default")
	}
}

// TestLoadMetricsIntervalClamp checks a non-positive interval is clamped to the
// default rather than passed through as a misleading value.
func TestLoadMetricsIntervalClamp(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}
	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_METRICS_INTERVAL", "0s")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MetricsInterval != 15*time.Second {
		t.Errorf("a 0 interval should clamp to the 15s default, got %v", c.MetricsInterval)
	}
}

// TestLoadDeploySilenceGrace exercises the deploy-silence-grace flag's full
// default/override/disable path THROUGH Load() itself — the maintenance
// window auto-silence tests construct config.Config directly, so they could
// not have caught a regression in this flag/env mapping (e.g. the default
// silently failing to reach the resolved Config, as a `> 0` guard around the
// assignment once did).
func TestLoadDeploySilenceGrace(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}
	t.Setenv("DC_DATA_DIR", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DeploySilenceGrace != 3*time.Minute {
		t.Errorf("default deploy silence grace = %v, want 3m", c.DeploySilenceGrace)
	}
}

func TestLoadDeploySilenceGraceEnvOverride(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}
	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_DEPLOY_SILENCE_GRACE", "10m")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DeploySilenceGrace != 10*time.Minute {
		t.Errorf("DC_DEPLOY_SILENCE_GRACE not mapped: got %v, want 10m", c.DeploySilenceGrace)
	}
}

// TestLoadDeploySilenceGraceExplicitZeroDisables pins the documented
// behaviour that 0 really means "off", not "use the default" — the flag's
// own help text promises this.
func TestLoadDeploySilenceGraceExplicitZeroDisables(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}
	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_DEPLOY_SILENCE_GRACE", "0s")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DeploySilenceGrace != 0 {
		t.Errorf("an explicit 0 should disable auto-silence, got %v", c.DeploySilenceGrace)
	}
}

func TestParseCIDRs(t *testing.T) {
	// Valid: a CIDR, a bare IPv4 (→ /32) and a bare IPv6 (→ /128), plus blanks.
	nets, err := parseCIDRs(" 10.0.0.0/8 , 127.0.0.1 ,, ::1 ")
	if err != nil {
		t.Fatalf("parseCIDRs: %v", err)
	}
	if len(nets) != 3 {
		t.Fatalf("want 3 nets, got %d (%v)", len(nets), nets)
	}
	if !nets[0].Contains(parseIP("10.1.2.3")) {
		t.Error("10.0.0.0/8 should contain 10.1.2.3")
	}
	if !nets[1].Contains(parseIP("127.0.0.1")) || nets[1].Contains(parseIP("127.0.0.2")) {
		t.Error("bare 127.0.0.1 should match only itself (/32)")
	}
	if !nets[2].Contains(parseIP("::1")) {
		t.Error("bare ::1 should match itself (/128)")
	}

	// Empty input → no networks, no error.
	if got, err := parseCIDRs(""); err != nil || len(got) != 0 {
		t.Errorf("empty input: %v %v", got, err)
	}
	// Garbage → error (fail fast on misconfiguration).
	for _, bad := range []string{"not-an-ip", "10.0.0.0/99", "1.2.3.4/foo"} {
		if _, err := parseCIDRs(bad); err == nil {
			t.Errorf("parseCIDRs(%q) should error", bad)
		}
	}
}

func parseIP(s string) net.IP { return net.ParseIP(s) }

func TestLoadConfigFileParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commander.conf")
	body := "# a comment\n\nDC_ADDR = 10.0.0.1:8080\nexport DC_REDIS_DB=3\nDC_METRICS_TOKEN=\"secret\"\nbad line without equals\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	vals, warnings, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if want := []string{path + ":6: not KEY=VALUE, ignored"}; !slices.Equal(warnings, want) {
		t.Errorf("warnings = %q, want %q", warnings, want)
	}
	if vals["DC_ADDR"] != "10.0.0.1:8080" {
		t.Errorf("trimmed value wrong: %q", vals["DC_ADDR"])
	}
	if vals["DC_REDIS_DB"] != "3" {
		t.Errorf("export prefix not stripped: %q", vals["DC_REDIS_DB"])
	}
	if vals["DC_METRICS_TOKEN"] != "secret" {
		t.Errorf("quotes not stripped: %q", vals["DC_METRICS_TOKEN"])
	}
	if _, ok := vals["bad line without equals"]; ok {
		t.Error("lines without '=' should be skipped")
	}
}

func TestLoadConfigFilePrecedence(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	dir := t.TempDir()
	path := filepath.Join(dir, "commander.conf")
	conf := "DC_ADDR=192.168.1.1:7000\nDC_DATA_DIR=" + dir + "\nDC_METRICS_RETENTION=15m\n"
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DC_CONFIG", path)
	// Env must win over the file for keys present in both.
	t.Setenv("DC_METRICS_RETENTION", "90m")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "192.168.1.1:7000" {
		t.Errorf("config-file value should be used when env is unset: %q", c.Addr)
	}
	if c.MetricsRetention != 90*time.Minute {
		t.Errorf("env should override the config file: %v", c.MetricsRetention)
	}
}

func TestLoadMissingExplicitConfigErrors(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.conf"))
	if _, err := Load(); err == nil {
		t.Error("an explicitly configured but missing config file should error")
	}
}

func TestLoadPortShorthand(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd", "-p", "9090"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Default host is loopback; -p overrides just the port.
	if c.Addr != "127.0.0.1:9090" {
		t.Errorf("-p should override the port keeping the host: %q", c.Addr)
	}
}

func TestLoadHostPort(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_HOST", "0.0.0.0")
	t.Setenv("DC_PORT", "9443")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "0.0.0.0:9443" {
		t.Errorf("host+port should compose the addr: %q", c.Addr)
	}
}

func TestLoadAddrOverridesHostPort(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_HOST", "0.0.0.0")
	t.Setenv("DC_PORT", "9443")
	t.Setenv("DC_ADDR", "127.0.0.1:7000") // legacy full address wins
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "127.0.0.1:7000" {
		t.Errorf("legacy DC_ADDR should override host+port: %q", c.Addr)
	}
}

func TestLoadTLSRequiresBoth(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_TLS_CERT", "/tmp/cert.pem") // key missing
	if _, err := Load(); err == nil {
		t.Error("a TLS cert without a key should error")
	}
}

func TestLoadTLSBothOK(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_TLS_CERT", "/tmp/cert.pem")
	t.Setenv("DC_TLS_KEY", "/tmp/key.pem")
	c, err := Load()
	if err != nil {
		t.Fatalf("both cert+key should be accepted: %v", err)
	}
	if c.TLSCert == "" || c.TLSKey == "" {
		t.Error("TLS paths should be set")
	}
}

func TestLoadACMEDomains(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	dir := t.TempDir()
	t.Setenv("DC_DATA_DIR", dir)
	t.Setenv("DC_ACME_DOMAINS", " example.com , www.example.com ")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"example.com", "www.example.com"}; !equalStrings(c.ACMEDomains, want) {
		t.Errorf("ACMEDomains = %v, want %v (whitespace should be trimmed)", c.ACMEDomains, want)
	}
	if want := filepath.Join(dir, "acme"); c.ACMECacheDir != want {
		t.Errorf("ACMECacheDir = %q, want default %q", c.ACMECacheDir, want)
	}
	if !c.TLSEnabled() {
		t.Error("TLSEnabled() should be true when ACME domains are set")
	}
}

func TestLoadACMECacheDirOverride(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_ACME_DOMAINS", "example.com")
	t.Setenv("DC_ACME_CACHE_DIR", "/var/lib/dockercmd/acme-cache")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ACMECacheDir != "/var/lib/dockercmd/acme-cache" {
		t.Errorf("ACMECacheDir = %q, want the explicit override preserved", c.ACMECacheDir)
	}
}

// PENTEST-adjacent (config-validation): ACME and a static cert/key pair are
// two different ways to obtain a certificate — combining them silently would
// leave it ambiguous which one actually serves, so this must be a hard error,
// not a "last one wins".
func TestLoadACMEConflictsWithStaticTLS(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_ACME_DOMAINS", "example.com")
	t.Setenv("DC_TLS_CERT", "/tmp/cert.pem")
	t.Setenv("DC_TLS_KEY", "/tmp/key.pem")
	if _, err := Load(); err == nil {
		t.Error("ACME domains combined with a static cert/key pair should error")
	}
}

func TestLoadACMERejectsIPAddress(t *testing.T) {
	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = []string{"dockercmd"}

	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_ACME_DOMAINS", "203.0.113.5")
	if _, err := Load(); err == nil {
		t.Error("an IP address in -acme-domains should error (ACME issues certs for hostnames only)")
	}
}

func TestConfigTLSEnabled(t *testing.T) {
	cases := []struct {
		name string
		c    Config
		want bool
	}{
		{"neither set", Config{}, false},
		{"static pair", Config{TLSCert: "/c", TLSKey: "/k"}, true},
		{"cert without key", Config{TLSCert: "/c"}, false},
		{"acme domains", Config{ACMEDomains: []string{"example.com"}}, true},
	}
	for _, tc := range cases {
		if got := tc.c.TLSEnabled(); got != tc.want {
			t.Errorf("%s: TLSEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDBPath(t *testing.T) {
	c := Config{DataDir: "/var/lib/dockercmd"}
	if got, want := c.DBPath(), "/var/lib/dockercmd/docker-commander.db"; got != want {
		t.Errorf("DBPath = %q want %q", got, want)
	}
}

func TestEnvOr(t *testing.T) {
	if got := envOr("DC_TEST_MISSING_XYZ", "fallback"); got != "fallback" {
		t.Errorf("missing env should return default, got %q", got)
	}
	t.Setenv("DC_TEST_PRESENT", "value")
	if got := envOr("DC_TEST_PRESENT", "fallback"); got != "value" {
		t.Errorf("present env should win, got %q", got)
	}
}

func TestEnvInt(t *testing.T) {
	if got := envInt("DC_TEST_INT_MISSING", 7); got != 7 {
		t.Errorf("missing → default, got %d", got)
	}
	t.Setenv("DC_TEST_INT", "42")
	if got := envInt("DC_TEST_INT", 7); got != 42 {
		t.Errorf("parsed int, got %d", got)
	}
	if len(settingErrs) != 0 {
		t.Fatalf("errors for valid input: %v", settingErrs)
	}
	t.Cleanup(func() { settingErrs = nil })
	t.Setenv("DC_TEST_INT_BAD", "notanumber")
	if got := envInt("DC_TEST_INT_BAD", 7); got != 7 {
		t.Errorf("unparseable → default, got %d", got)
	}
	if len(settingErrs) != 1 || !strings.Contains(settingErrs[0].err.Error(), "DC_TEST_INT_BAD") {
		t.Errorf("unparseable value not recorded: %v", settingErrs)
	}
}

func TestEnvDuration(t *testing.T) {
	if got := envDuration("DC_TEST_DUR_MISSING", 6*time.Hour); got != 6*time.Hour {
		t.Errorf("missing → default, got %v", got)
	}
	t.Setenv("DC_TEST_DUR", "90m")
	if got := envDuration("DC_TEST_DUR", 6*time.Hour); got != 90*time.Minute {
		t.Errorf("parsed duration, got %v", got)
	}
	if len(settingErrs) != 0 {
		t.Fatalf("errors for valid input: %v", settingErrs)
	}
	t.Cleanup(func() { settingErrs = nil })
	t.Setenv("DC_TEST_DUR_BAD", "1d")
	if got := envDuration("DC_TEST_DUR_BAD", 6*time.Hour); got != 6*time.Hour {
		t.Errorf("unparseable → default, got %v", got)
	}
	if len(settingErrs) != 1 || !strings.Contains(settingErrs[0].err.Error(), "DC_TEST_DUR_BAD") {
		t.Errorf("unparseable value not recorded: %v", settingErrs)
	}
}

func TestEnvBool(t *testing.T) {
	t.Cleanup(func() { settingErrs = nil })
	for _, def := range []bool{true, false} {
		if got := envBool("DC_TEST_BOOL_MISSING", def); got != def {
			t.Errorf("missing → default %v, got %v", def, got)
		}
	}
	for v, want := range map[string]bool{
		"1": true, "true": true, "TRUE": true, "yes": true, "On": true,
		"0": false, "false": false, "False": false, "no": false, "OFF": false,
	} {
		t.Setenv("DC_TEST_BOOL", v)
		// The opposite default, so a value that fell back would show.
		if got := envBool("DC_TEST_BOOL", !want); got != want {
			t.Errorf("%q → %v, want %v", v, got, want)
		}
	}
	if len(settingErrs) != 0 {
		t.Fatalf("errors for valid input: %v", settingErrs)
	}
	t.Setenv("DC_TEST_BOOL", "disabled")
	if got := envBool("DC_TEST_BOOL", true); got != true {
		t.Errorf("unparseable → default, got %v", got)
	}
	if len(settingErrs) != 1 || !strings.Contains(settingErrs[0].err.Error(), "DC_TEST_BOOL") {
		t.Errorf("unparseable value not recorded: %v", settingErrs)
	}
}

// loadWith runs Load with only the given environment (plus a temp data dir)
// and no config file unless env names one in DC_CONFIG.
func loadWith(t *testing.T, env map[string]string, args ...string) (Config, error) {
	t.Helper()
	oldArgs, oldFS := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFS })
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = append([]string{"dockercmd"}, args...)
	t.Setenv("DC_DATA_DIR", t.TempDir())
	t.Setenv("DC_CONFIG", filepath.Join(t.TempDir(), "none.conf"))
	for k, v := range env {
		t.Setenv(k, v)
	}
	if _, ok := env["DC_CONFIG"]; !ok {
		// An explicit DC_CONFIG must exist; point at an empty one.
		if err := os.WriteFile(os.Getenv("DC_CONFIG"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load()
}

// A setting that doesn't parse used to fall back to its default without a
// word. For the on/off switches that default is often "on": DC_SELF_UPDATE=false
// left self-update enabled. Each must now stop startup and name the setting.
func TestLoadRejectsUnparseableSettings(t *testing.T) {
	for key, v := range map[string]string{
		"DC_DEV":                  "yes please",
		"DC_UPDATE_CHECK":         "disabled",
		"DC_SELF_UPDATE":          "never",
		"DC_MCP_ENABLED":          "enable",
		"DC_PROXY_ENABLED":        "2",
		"DC_PPROF":                "y",
		"DC_PORT":                 "84a0",
		"DC_REDIS_DB":             "first",
		"DC_METRICS_RETENTION":    "1d",
		"DC_METRICS_INTERVAL":     "15",
		"DC_DEPLOY_SILENCE_GRACE": "soon",
		"DC_FORWARDED_PREFIX":     "nope",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := loadWith(t, map[string]string{key: v})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%q: want an error naming it, got %v", key, v, err)
			}
		})
	}
}

// A flag on the command line wins over the environment and the config file, so
// a bad value it overrides never applies and must not stop startup. Every typed
// setting that has a flag is listed, for both lower layers.
func TestLoadFlagOverridesUnparseableSetting(t *testing.T) {
	cases := []struct {
		key, bad, flag string
		check          func(Config) bool
	}{
		{"DC_PORT", "bad", "--port=18470", func(c Config) bool { return strings.HasSuffix(c.Addr, ":18470") }},
		{"DC_PORT", "bad", "-p=18470", func(c Config) bool { return strings.HasSuffix(c.Addr, ":18470") }},
		{"DC_DEV", "maybe", "-dev=false", func(c Config) bool { return !c.Dev }},
		{"DC_UPDATE_CHECK", "maybe", "-update-check=false", func(c Config) bool { return !c.UpdateCheck }},
		{"DC_SELF_UPDATE", "maybe", "-self-update=false", func(c Config) bool { return !c.SelfUpdate }},
		{"DC_MCP_ENABLED", "maybe", "-mcp-enabled", func(c Config) bool { return c.MCPEnabled }},
		{"DC_PROXY_ENABLED", "maybe", "-proxy-enabled", func(c Config) bool { return c.ProxyEnabled }},
		{"DC_PPROF", "maybe", "-pprof", func(c Config) bool { return c.PProf }},
		{"DC_FORWARDED_PREFIX", "maybe", "-forwarded-prefix=false", func(c Config) bool { return !c.ForwardedPrefix }},
		{"DC_METRICS_RETENTION", "1d", "-metrics-retention=24h", func(c Config) bool { return c.MetricsRetention == 24*time.Hour }},
		{"DC_METRICS_INTERVAL", "15", "-metrics-interval=30s", func(c Config) bool { return c.MetricsInterval == 30*time.Second }},
		{"DC_DEPLOY_SILENCE_GRACE", "soon", "-deploy-silence-grace=1m", func(c Config) bool { return c.DeploySilenceGrace == time.Minute }},
	}
	for _, tc := range cases {
		t.Run(tc.key+" env "+tc.flag, func(t *testing.T) {
			c, err := loadWith(t, map[string]string{tc.key: tc.bad}, tc.flag)
			if err != nil || !tc.check(c) {
				t.Errorf("%s=%q with %s: err %v, flag value not applied", tc.key, tc.bad, tc.flag, err)
			}
		})
		t.Run(tc.key+" file "+tc.flag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commander.conf")
			if err := os.WriteFile(path, []byte(tc.key+"="+tc.bad+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := loadWith(t, map[string]string{"DC_CONFIG": path}, tc.flag)
			if err != nil || !tc.check(c) {
				t.Errorf("%s=%q in the file with %s: err %v, flag value not applied", tc.key, tc.bad, tc.flag, err)
			}
		})
	}
	// A flag overrides only its own setting: another bad one still fails.
	if _, err := loadWith(t, map[string]string{"DC_PORT": "bad", "DC_SELF_UPDATE": "maybe"}, "--port=18470"); err == nil ||
		!strings.Contains(err.Error(), "DC_SELF_UPDATE") || strings.Contains(err.Error(), "DC_PORT") {
		t.Errorf("want only DC_SELF_UPDATE reported, got %v", err)
	}
}

// Every typed setting that has a flag must be defined through boolFlag,
// intFlag or durationFlag, or the override above silently stops working for
// it. DC_REDIS_DB is the one typed setting without a flag.
func TestTypedSettingsWithFlagsAreTracked(t *testing.T) {
	bad := map[string]string{}
	for _, k := range []string{"DC_DEV", "DC_UPDATE_CHECK", "DC_SELF_UPDATE", "DC_MCP_ENABLED", "DC_PROXY_ENABLED", "DC_PPROF", "DC_FORWARDED_PREFIX"} {
		bad[k] = "maybe"
	}
	for _, k := range []string{"DC_PORT", "DC_REDIS_DB"} {
		bad[k] = "x"
	}
	for _, k := range []string{"DC_METRICS_RETENTION", "DC_METRICS_INTERVAL", "DC_DEPLOY_SILENCE_GRACE"} {
		bad[k] = "x"
	}
	if _, err := loadWith(t, bad); err == nil {
		t.Fatal("want an error")
	}
	tracked := map[string]bool{}
	for _, k := range flagKeys {
		tracked[k] = true
	}
	for _, e := range settingErrs {
		if !tracked[e.key] && e.key != "DC_REDIS_DB" {
			t.Errorf("%s is parsed but no flag is recorded as overriding it", e.key)
		}
	}
	if len(settingErrs) != len(bad) {
		t.Errorf("parsed %d typed settings, the test lists %d", len(settingErrs), len(bad))
	}
}

// Every bad setting is reported at once, not one per restart.
func TestLoadReportsAllUnparseableSettings(t *testing.T) {
	_, err := loadWith(t, map[string]string{"DC_SELF_UPDATE": "nope", "DC_METRICS_RETENTION": "1d"})
	if err == nil || !strings.Contains(err.Error(), "DC_SELF_UPDATE") || !strings.Contains(err.Error(), "DC_METRICS_RETENTION") {
		t.Errorf("want both settings named, got %v", err)
	}
}

func TestLoadBoolSpellings(t *testing.T) {
	c, err := loadWith(t, map[string]string{"DC_SELF_UPDATE": "false", "DC_UPDATE_CHECK": "off", "DC_MCP_ENABLED": "true", "DC_PPROF": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SelfUpdate || c.UpdateCheck || !c.MCPEnabled || !c.PProf {
		t.Errorf("self-update %v, update check %v, MCP %v, pprof %v; want off, off, on, on",
			c.SelfUpdate, c.UpdateCheck, c.MCPEnabled, c.PProf)
	}
}

func TestLoadACMEDirectoryURL(t *testing.T) {
	c, err := loadWith(t, map[string]string{"DC_ACME_DIRECTORY_URL": "https://acme-staging-v02.api.letsencrypt.org/directory"})
	if err != nil || c.ACMEDirectoryURL == "" {
		t.Fatalf("staging directory: %v", err)
	}
	for _, v := range []string{"http://acme.example/directory", "acme.example/directory", "https:///directory", "https://[::1"} {
		t.Run(v, func(t *testing.T) {
			if _, err := loadWith(t, map[string]string{"DC_ACME_DIRECTORY_URL": v}); err == nil || !strings.Contains(err.Error(), "-acme-directory-url") {
				t.Errorf("%q: want a startup error, got %v", v, err)
			}
		})
	}
}

// A misspelt key in the config file is reported, not silently ignored. Keys
// without the DC_ prefix belong to whatever else reads the file.
func TestLoadWarnsAboutUnreadFileKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commander.conf")
	body := "DC_SELFUPDATE=0\nDC_PORT=9000\nDOCKER_HOST=unix:///run/docker.sock\nDC_MCP_ENABLED 1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadWith(t, map[string]string{"DC_CONFIG": path})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		path + ":4: not KEY=VALUE, ignored",
		path + ": unknown setting DC_SELFUPDATE is ignored",
	}
	if !slices.Equal(c.Warnings, want) {
		t.Errorf("warnings = %q, want %q", c.Warnings, want)
	}
	if !strings.HasSuffix(c.Addr, ":9000") {
		t.Errorf("DC_PORT from the file not applied: %s", c.Addr)
	}
}

func TestDefaultDataDir(t *testing.T) {
	if defaultDataDir() == "" {
		t.Error("defaultDataDir should never be empty")
	}
}

// The embedded reverse proxy's own settings: off unless DC_PROXY_ENABLED=1,
// and its ACME state defaults to a directory SEPARATE from the admin one — a
// shared directory would let the two autocert managers overwrite each
// other's account key and certificates.
func loadProxyConfig(t *testing.T, env map[string]string, args ...string) Config {
	t.Helper()
	oldArgs, oldFS := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFS })
	flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
	os.Args = append([]string{"dockercmd"}, args...)
	for k, v := range env {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestLoadProxyDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	c := loadProxyConfig(t, map[string]string{"DC_DATA_DIR": dir})
	if c.ProxyEnabled {
		t.Error("the proxy is a second public-facing surface and must be off by default")
	}
	if want := filepath.Join(dir, "proxy-acme"); c.ProxyACMECacheDir != want {
		t.Errorf("ProxyACMECacheDir = %q, want the default %q", c.ProxyACMECacheDir, want)
	}
	if c.ProxyACMECacheDir == filepath.Join(dir, "acme") {
		t.Error("the proxy's ACME state must never default to the admin ACME directory")
	}
}

func TestLoadProxyEnabledFromEnvAndFlag(t *testing.T) {
	// The same on/off spellings as every switch; one that isn't among them
	// fails startup (TestLoadRejectsUnparseableSettings).
	if c := loadProxyConfig(t, map[string]string{"DC_DATA_DIR": t.TempDir(), "DC_PROXY_ENABLED": "1"}); !c.ProxyEnabled {
		t.Error("DC_PROXY_ENABLED=1 should enable the proxy")
	}
	if c := loadProxyConfig(t, map[string]string{"DC_DATA_DIR": t.TempDir(), "DC_PROXY_ENABLED": "true"}); !c.ProxyEnabled {
		t.Error("DC_PROXY_ENABLED=true should enable the proxy")
	}
	if c := loadProxyConfig(t, map[string]string{"DC_DATA_DIR": t.TempDir(), "DC_PROXY_ENABLED": "0"}); c.ProxyEnabled {
		t.Error("DC_PROXY_ENABLED=0 must leave the proxy off")
	}
	if c := loadProxyConfig(t, map[string]string{"DC_DATA_DIR": t.TempDir()}, "-proxy-enabled"); !c.ProxyEnabled {
		t.Error("-proxy-enabled should enable the proxy")
	}
}

func TestLoadProxyACMECacheDirOverride(t *testing.T) {
	c := loadProxyConfig(t, map[string]string{"DC_DATA_DIR": t.TempDir(), "DC_PROXY_ACME_CACHE_DIR": "/var/lib/dockercmd/proxy-cache"})
	if c.ProxyACMECacheDir != "/var/lib/dockercmd/proxy-cache" {
		t.Errorf("ProxyACMECacheDir = %q, want the explicit override preserved", c.ProxyACMECacheDir)
	}
}

func TestNormalizePathPrefix(t *testing.T) {
	ok := map[string]string{
		"":                "",
		"/":               "",
		" /dockercmd/ ":   "/dockercmd",
		"dockercmd":       "/dockercmd",
		"/a/b-c_d.e~f/":   "/a/b-c_d.e~f",
		"/Docker-Cmd2026": "/Docker-Cmd2026",
	}
	for in, want := range ok {
		if got, err := NormalizePathPrefix(in); err != nil || got != want {
			t.Errorf("NormalizePathPrefix(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// Each of these would be dangerous written into <base href>, a cookie
	// path or a form action: another host, markup, a traversal, an encoded
	// separator, a CRLF.
	bad := []string{
		"//evil.example", "///x", "/a//b", "https://evil.example", "javascript:alert(1)",
		`/"><script>alert(1)</script>`, "/a b", "/a/../b", "/..", "/.", "/a%2fb",
		`\evil.example`, "/a\r\nSet-Cookie: x=1", "/ěšč", "/a?b", "/a#b",
		"/" + strings.Repeat("a", maxPathPrefix),
	}
	for _, in := range bad {
		if got, err := NormalizePathPrefix(in); err == nil {
			t.Errorf("NormalizePathPrefix(%q) = %q, want an error", in, got)
		}
	}
}

func TestLoadPathPrefix(t *testing.T) {
	load := func(t *testing.T, env map[string]string) (Config, error) {
		oldArgs, oldFS := os.Args, flag.CommandLine
		t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFS })
		flag.CommandLine = flag.NewFlagSet("dockercmd", flag.ContinueOnError)
		os.Args = []string{"dockercmd"}
		t.Setenv("DC_DATA_DIR", t.TempDir())
		for k, v := range env {
			t.Setenv(k, v)
		}
		return Load()
	}
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.BasePath != "" || !c.ForwardedPrefix {
		t.Errorf("defaults: base path %q, forwarded prefix %v; want root and on", c.BasePath, c.ForwardedPrefix)
	}
	c, err = load(t, map[string]string{"DC_BASE_PATH": "/dockercmd/", "DC_FORWARDED_PREFIX": "0"})
	if err != nil {
		t.Fatal(err)
	}
	if c.BasePath != "/dockercmd" || c.ForwardedPrefix {
		t.Errorf("env: base path %q, forwarded prefix %v; want /dockercmd and off", c.BasePath, c.ForwardedPrefix)
	}
	if _, err := load(t, map[string]string{"DC_BASE_PATH": "//evil.example"}); err == nil {
		t.Error("an invalid DC_BASE_PATH must fail startup, not be ignored")
	}
	// DC_BASE_PATH is reset: t.Setenv lasts for the whole test, and the bad
	// value above would fail startup on its own.
	_, err = load(t, map[string]string{"DC_BASE_PATH": "", "DC_MCP_PUBLIC_URL": "https://host.example/a b"})
	if err == nil || !strings.Contains(err.Error(), "-mcp-public-url") {
		t.Errorf("a DC_MCP_PUBLIC_URL path that isn't a plain prefix must fail startup, got %v", err)
	}
}

func TestPublicURLPath(t *testing.T) {
	good := map[string]string{
		"":                                "",
		"https://host.example":            "",
		"https://host.example/":           "",
		"https://host.example:8443/dc":    "/dc",
		"https://host.example/dockercmd/": "/dockercmd",
		"https://host.example/a/b":        "/a/b",
	}
	for in, want := range good {
		if got, err := PublicURLPath(in); err != nil || got != want {
			t.Errorf("PublicURLPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// The path becomes a route pattern and the OAuth issuer: chi reads {x}
	// and * as wildcards, and an escape or dot-segment isn't a plain path.
	for _, in := range []string{
		"https://host.example/{x}", "https://host.example/a*", "https://host.example/a%2Fb",
		"https://host.example/a/../b", "https://host.example//a", "https://host.example/a?x=1",
		"https://host.example/a#f", "https://host.example/a?", "http://[::1",
		"docker.example.com", "docker.example.com/dockercmd", "//host.example/a",
		"ftp://host.example/a", "https:///a", "https://",
	} {
		if got, err := PublicURLPath(in); err == nil {
			t.Errorf("PublicURLPath(%q) = %q, want an error", in, got)
		}
	}
}
