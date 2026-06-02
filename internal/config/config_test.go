package config

import (
	"flag"
	"os"
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
