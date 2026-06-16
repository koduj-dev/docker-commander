package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// composeTimeout bounds a deploy/down. `up -d` still waits for image pulls,
// builds and depends_on healthchecks, so it can legitimately take minutes.
const composeTimeout = 10 * time.Minute

// ComposeAvailable reports whether the `docker compose` CLI is usable on the
// host running Docker Commander. This is a runtime dependency that the SDK-only
// rest of the app doesn't need, so callers degrade gracefully when it's false.
// The result is cached for the process lifetime — it's called on every project
// list and we don't want to fork `docker compose version` on each request.
var (
	composeOnce sync.Once
	composeOK   bool
)

// ComposeAvailable reports whether the `docker compose` CLI is usable on the
// host. The result is probed once and cached for the process lifetime.
func ComposeAvailable(ctx context.Context) bool {
	// Probe with a fresh background context (its own timeout below), not the
	// caller's: a cancelled/timed-out first request must not cache "unavailable"
	// for the whole process lifetime via sync.Once.
	_ = ctx
	composeOnce.Do(func() { composeOK = composeProbe(context.Background(), "docker") })
	return composeOK
}

// composeProbe runs `<bin> compose version`; split out so tests can exercise the
// not-found path with a bogus binary.
func composeProbe(ctx context.Context, bin string) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, bin, "compose", "version").Run() == nil
}

// ComposeUp runs `docker compose -p <slug> [--profile p…] up -d` in dir and
// returns the combined stdout+stderr (for display) alongside any error. env adds
// to the process environment (e.g. DOCKER_HOST to target a remote daemon); nil
// runs against the local daemon.
func ComposeUp(ctx context.Context, dir, slug string, profiles []string, env []string) (string, error) {
	args := make([]string, 0, len(profiles)*2+2)
	for _, p := range profiles {
		if p = strings.TrimSpace(p); p != "" {
			args = append(args, "--profile", p)
		}
	}
	args = append(args, "up", "-d")
	return runCompose(ctx, dir, slug, env, args...)
}

// ComposeProfiles lists the profiles defined in the project's compose file
// (`docker compose config --profiles`), one per line.
func ComposeProfiles(ctx context.Context, dir, slug string) ([]string, error) {
	out, err := runCompose(ctx, dir, slug, nil, "config", "--profiles")
	if err != nil {
		return nil, err
	}
	var profiles []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			profiles = append(profiles, line)
		}
	}
	return profiles, nil
}

// ComposeConfig validates the project's compose file via
// `docker compose config --quiet` — the same parser used to deploy, so YAML
// anchors/aliases, merge keys (`<<`), `${VAR}` interpolation and
// `extends`/`include` resolve exactly as they will at `up` time. On success it
// prints nothing; on failure the combined output carries the error (often with
// a file/line reference).
func ComposeConfig(ctx context.Context, dir, slug string) (string, error) {
	return runCompose(ctx, dir, slug, nil, "config", "--quiet")
}

// ComposeResolvedConfig returns the fully-resolved compose configuration
// (`docker compose config` without --quiet): anchors, merge keys, ${VAR}
// interpolation and extends/include flattened into one canonical YAML — exactly
// what `up` will deploy. Only stdout (the YAML) is returned; on failure the
// error carries stderr.
func ComposeResolvedConfig(ctx context.Context, dir, slug string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, composeTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "compose", "-p", slug, "config")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

// ComposeConfigJSON returns the resolved compose model as JSON
// (`docker compose config --format json`) — used to build a project overview
// (services, ports, volumes) and detect issues like duplicate host ports.
func ComposeConfigJSON(ctx context.Context, dir, slug string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, composeTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "compose", "-p", slug, "config", "--format", "json")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// ComposeWarnings extracts the human-readable messages from `level=warning`
// lines in compose CLI output (e.g. `The "X" variable is not set`), which the
// CLI prints to stderr even for an otherwise-valid file.
func ComposeWarnings(out string) []string {
	var ws []string
	for _, ln := range strings.Split(out, "\n") {
		if !strings.Contains(ln, "level=warning") {
			continue
		}
		i := strings.Index(ln, `msg="`)
		if i < 0 {
			continue
		}
		rest := ln[i+len(`msg="`):]
		j := strings.LastIndex(rest, `"`)
		if j < 0 {
			continue
		}
		if msg := strings.TrimSpace(strings.ReplaceAll(rest[:j], `\"`, `"`)); msg != "" {
			ws = append(ws, msg)
		}
	}
	return ws
}

// ComposeDown runs `docker compose -p <slug> down` in dir (removes containers
// and the project's networks; named volumes are kept, like the CLI default).
func ComposeDown(ctx context.Context, dir, slug string, env []string) (string, error) {
	return runCompose(ctx, dir, slug, env, "down")
}

// ComposeRestart runs `docker compose -p <slug> restart` (restarts the running
// containers without re-creating them).
func ComposeRestart(ctx context.Context, dir, slug string, env []string) (string, error) {
	return runCompose(ctx, dir, slug, env, "restart")
}

func runCompose(ctx context.Context, dir, slug string, env []string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, composeTimeout)
	defer cancel()
	full := append([]string{"compose", "-p", slug}, args...)
	cmd := exec.CommandContext(cctx, "docker", full...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
