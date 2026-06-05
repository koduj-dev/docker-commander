package docker

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// composeTimeout bounds a deploy/down. `up -d` still waits for image pulls,
// builds and depends_on healthchecks, so it can legitimately take minutes.
const composeTimeout = 10 * time.Minute

// ComposeAvailable reports whether the `docker compose` CLI is usable on the
// host running Docker Commander. This is a runtime dependency that the SDK-only
// rest of the app doesn't need, so callers degrade gracefully when it's false.
func ComposeAvailable(ctx context.Context) bool {
	return composeProbe(ctx, "docker")
}

// composeProbe runs `<bin> compose version`; split out so tests can exercise the
// not-found path with a bogus binary.
func composeProbe(ctx context.Context, bin string) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, bin, "compose", "version").Run() == nil
}

// ComposeUp runs `docker compose -p <slug> up -d` in dir and returns the
// combined stdout+stderr (for display) alongside any error.
func ComposeUp(ctx context.Context, dir, slug string) (string, error) {
	return runCompose(ctx, dir, slug, "up", "-d")
}

// ComposeDown runs `docker compose -p <slug> down` in dir (removes containers
// and the project's networks; named volumes are kept, like the CLI default).
func ComposeDown(ctx context.Context, dir, slug string) (string, error) {
	return runCompose(ctx, dir, slug, "down")
}

func runCompose(ctx context.Context, dir, slug string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, composeTimeout)
	defer cancel()
	full := append([]string{"compose", "-p", slug}, args...)
	cmd := exec.CommandContext(cctx, "docker", full...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
