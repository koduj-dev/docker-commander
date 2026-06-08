package docker

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dockerfileCheckTimeout bounds a `docker build --check`. The check itself runs
// no build steps, but it resolves base-image metadata from the registry, so it
// can take a second or two (longer for a slow/large base).
const dockerfileCheckTimeout = 60 * time.Second

var (
	buildxOnce sync.Once
	buildxOK   bool
)

// BuildCheckAvailable reports whether `docker build --check` (BuildKit's
// Dockerfile linter) is usable. Probed once and cached for the process lifetime.
func BuildCheckAvailable(ctx context.Context) bool {
	_ = ctx
	buildxOnce.Do(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		buildxOK = exec.CommandContext(cctx, "docker", "buildx", "version").Run() == nil
	})
	return buildxOK
}

// DockerfileCheck lints a Dockerfile with `docker build --check` without running
// any build steps. The content is written to a throwaway build context (the
// check doesn't read COPY/ADD sources, so the Dockerfile alone is enough). The
// cleaned check output is returned with BuildKit progress noise stripped; a
// non-nil error means the check reported problems — lint warnings (exit 255) or
// a parse error (exit 1) — with the detail in the returned string.
func DockerfileCheck(ctx context.Context, content string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, dockerfileCheckTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "dc-dfcheck-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(content), 0o600); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(cctx, "docker", "build", "--check", "-f", "Dockerfile", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DOCKER_CLI_HINTS=false")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	return cleanBuildCheck(buf.String()), runErr
}

// cleanBuildCheck strips BuildKit progress lines (#0/#1/… and their blank
// padding), keeping the "Check complete…" verdict and any WARNING/ERROR blocks.
func cleanBuildCheck(s string) string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimRight(ln, " \t")
		if strings.HasPrefix(t, "#") { // BuildKit step/progress line
			continue
		}
		out = append(out, t)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
