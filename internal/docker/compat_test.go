package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Version compatibility.
//
// The app talks to Docker two ways: the Engine API through the Go SDK, and the
// `docker compose` CLI as a subprocess. Both move, and users run whatever their
// distro ships — so "works on my daemon" is not an answer to "which versions is
// this tested against?".
//
// The version matrix (.github/workflows/compat.yml) points DC_COMPAT_DOCKER at a
// pinned docker:NN-dind and runs the whole integration suite against it. This
// file adds the two things that suite can't express: what was actually
// negotiated, and the floor below which we refuse to guess.

// minAPIVersion is the oldest Engine API the app claims to support. The SDK
// negotiates down to the daemon, so a newer client talks to an older daemon
// fine — but only down to a point, and below this we neither test nor claim.
//
// 1.43 is Engine 24, the oldest release still receiving updates when host
// scoping shipped. Raising this is a compatibility break: say so in the
// changelog and in the README table.
const minAPIVersion = "1.43"

// TestCompatNegotiatedVersions records what the app negotiated with the daemon
// under test and fails if it falls below the documented floor. The workflow
// greps the COMPAT= line out of the log to build the README's table, so the
// numbers there are observed rather than remembered.
func TestCompatNegotiatedVersions(t *testing.T) {
	m, ctx := newManager(t)

	cli, err := m.Client(ctx, 0)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	info, err := m.SystemInfo(ctx, 0)
	if err != nil {
		t.Fatalf("system info: %v", err)
	}
	api := cli.ClientVersion() // the version negotiated with THIS daemon
	compose := composeVersion(ctx)

	t.Logf("COMPAT= engine=%s api=%s compose=%s os=%s arch=%s",
		info.ServerVersion, api, compose, info.OSType, info.Architecture)

	if api == "" {
		t.Fatal("no API version was negotiated")
	}
	if compareAPIVersions(api, minAPIVersion) < 0 {
		t.Errorf("negotiated API %s is below the documented floor %s — either the daemon is older than we claim to support, or the floor in README needs revisiting",
			api, minAPIVersion)
	}
	if compose == "" {
		t.Log("no docker compose CLI on PATH; the Compose-dependent tests will skip")
	}
}

// composeVersion returns the `docker compose version` short string, or "" when
// the plugin isn't installed.
func composeVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "docker", "compose", "version", "--short").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// compareAPIVersions compares dotted major.minor Engine API versions, returning
// -1, 0 or 1. Written out rather than pulled in: the SDK's own helper is not
// exported, and this is two integers.
func compareAPIVersions(a, b string) int {
	amaj, amin := splitAPIVersion(a)
	bmaj, bmin := splitAPIVersion(b)
	switch {
	case amaj != bmaj:
		if amaj < bmaj {
			return -1
		}
		return 1
	case amin != bmin:
		if amin < bmin {
			return -1
		}
		return 1
	}
	return 0
}

func splitAPIVersion(v string) (major, minor int) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) > 0 {
		major, _ = strconv.Atoi(parts[0])
	}
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major, minor
}

func TestCompareAPIVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.43", "1.43", 0},
		{"1.44", "1.43", 1},
		{"1.42", "1.43", -1},
		{"1.50", "1.9", 1},    // minor is numeric, not lexical
		{"2.0", "1.99", 1},    // major wins
		{"1.43", "", 1},       // an empty floor never blocks
		{" 1.43 ", "1.43", 0}, // the SDK can hand back padding
	}
	for _, c := range cases {
		if got := compareAPIVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestCompatComposeSurface exercises the `docker compose` subcommands the app
// shells out to, against whichever daemon is under test. The app depends on
// these being present AND on their output shape (`config --format json` in
// particular), which has changed across Compose releases — a version that drops
// or reshapes one of them breaks project deploys, not just a test.
func TestCompatComposeSurface(t *testing.T) {
	// newManager only for its side effects here: it skips the test when no daemon
	// is reachable, and gives us its context. The subject is the CLI, not the SDK.
	_, ctx := newManager(t)
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}

	const compose = `services:
  hello:
    image: alpine:latest
    command: ["true"]
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"config", []string{"config"}},
		{"config --format json", []string{"config", "--format", "json"}},
		{"config --services", []string{"config", "--services"}},
		{"ps --format json", []string{"ps", "--format", "json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"compose", "-p", "dc-compat"}, tc.args...)
			cmd := exec.CommandContext(ctx, "docker", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("docker %s failed on this Compose version: %v\n%s",
					strings.Join(args, " "), err, out)
			}
		})
	}
}
