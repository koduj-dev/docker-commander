package config

import (
	"flag"
	"net"
	"os"
	"path/filepath"
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

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "0.0.0.0:9999" || c.DataDir != dir || c.RedisDB != 2 || c.MetricsRetention != 45*time.Minute {
		t.Errorf("Load mapped env wrong: %+v", c)
	}
	if c.SessionTTL != 12*time.Hour {
		t.Errorf("default session TTL: %v", c.SessionTTL)
	}
	if len(c.TrustedProxies) != 0 {
		t.Errorf("trusted proxies should be empty by default, got %v", c.TrustedProxies)
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
	vals, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("loadConfigFile: %v", err)
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
	t.Setenv("DC_TEST_INT_BAD", "notanumber")
	if got := envInt("DC_TEST_INT_BAD", 7); got != 7 {
		t.Errorf("unparseable → default, got %d", got)
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
	t.Setenv("DC_TEST_DUR_BAD", "nonsense")
	if got := envDuration("DC_TEST_DUR_BAD", 6*time.Hour); got != 6*time.Hour {
		t.Errorf("unparseable → default, got %v", got)
	}
}

func TestDefaultDataDir(t *testing.T) {
	if defaultDataDir() == "" {
		t.Error("defaultDataDir should never be empty")
	}
}
