package docker

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// dockerCLI runs the docker CLI with extra env and returns combined output.
func dockerCLI(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// End to end against a real password-protected registry and the real compose
// CLI: without the generated config a pull is refused, with it the pull works.
//
// The user's own config names a credential helper that doesn't exist, so the
// pull only succeeds if the stored credential really overrides it. That is the
// part a unit test can't prove: it depends on how the Docker CLI picks a
// credential store, not on what we write.
func TestComposePullsAPrivateImageWithStoredCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	ctx := context.Background()
	if !ComposeAvailable(ctx) {
		t.Skip("docker compose not available")
	}
	if out, err := dockerCLI(ctx, nil, "image", "inspect", "registry:2", "busybox:latest"); err != nil {
		t.Skipf("needs registry:2 and busybox:latest locally: %s", out)
	}

	_, ref, auth := privateRegistry(t, ctx)
	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{auth})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	dir := t.TempDir()
	compose := fmt.Sprintf("services:\n  app:\n    image: %s\n    command: [\"sleep\", \"1\"]\n", ref)
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("dctest-regauth-%d", time.Now().UnixNano())

	if out, err := runComposeFiles(ctx, dir, project, nil, nil, "pull"); err == nil {
		t.Fatalf("the pull worked without stored credentials, so this test proves nothing: %s", out)
	}
	if out, err := runComposeFiles(ctx, dir, project, env, nil, "pull"); err != nil {
		t.Fatalf("pull with the stored credentials failed: %v: %s", err, out)
	}
}

// privateRegistry starts a registry that requires user "dctest" / password
// "pw-123", makes the CLI config at DOCKER_CONFIG name a credential helper that
// doesn't exist, pushes busybox into the registry and removes the local copy.
// It returns the registry address, the image reference, and the stored
// credential for it.
func privateRegistry(t *testing.T, ctx context.Context) (addr, ref string, auth store.RegistryAuth) {
	t.Helper()
	// A registry that requires user "dctest" / password "pw-123".
	authDir := t.TempDir()
	hash, err := bcrypt.GenerateFromPassword([]byte("pw-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "htpasswd"), []byte("dctest:"+string(hash)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dctest-authreg-%d", time.Now().UnixNano())
	if out, err := dockerCLI(ctx, nil, "run", "-d", "--name", name, "-p", "127.0.0.1::5000",
		"-v", authDir+":/auth:ro",
		"-e", "REGISTRY_AUTH=htpasswd", "-e", "REGISTRY_AUTH_HTPASSWD_REALM=dctest",
		"-e", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd", "registry:2"); err != nil {
		t.Fatalf("start registry: %v: %s", err, out)
	}
	t.Cleanup(func() { _, _ = dockerCLI(context.Background(), nil, "rm", "-f", name) })
	out, err := dockerCLI(ctx, nil, "port", name, "5000/tcp")
	if err != nil {
		t.Fatalf("registry port: %v: %s", err, out)
	}
	addr = strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	ref = addr + "/dctest/bb:1"

	// The user's own config: a credential store that doesn't exist.
	userCfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(userCfg, "config.json"),
		[]byte(`{"credsStore":"dctest-no-such-helper"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", userCfg)

	auth = store.RegistryAuth{Address: addr, Username: "dctest", Password: "pw-123"}
	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{auth})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Seed the registry (the registry may need a moment to accept connections).
	if out, err := dockerCLI(ctx, nil, "tag", "busybox:latest", ref); err != nil {
		t.Fatalf("tag: %v: %s", err, out)
	}
	// Registered before the push, so a failed push doesn't leave the tag behind.
	t.Cleanup(func() { _, _ = dockerCLI(context.Background(), nil, "rmi", "-f", ref) })
	var pushOut string
	for i := 0; i < 20; i++ {
		if pushOut, err = dockerCLI(ctx, env, "push", ref); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("push with the generated config: %v: %s", err, pushOut)
	}
	if out, err := dockerCLI(ctx, nil, "rmi", ref); err != nil {
		t.Fatalf("rmi: %v: %s", err, out)
	}

	return addr, ref, auth
}

// Redeploying a CLI-discovered stack on the local daemon uses the stored
// credentials too. The service sets `pull_policy: always`, so the redeploy must
// pull again, and the user's config only names a helper that doesn't exist.
func TestIntegrationStackRedeployUsesStoredCredentials(t *testing.T) {
	requireLocalDaemon(t)
	if testing.Short() {
		t.Skip("needs a docker daemon and the compose CLI; skipped under -short")
	}
	m, ctx := newManager(t)
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}
	if out, err := dockerCLI(ctx, nil, "image", "inspect", "registry:2", "busybox:latest"); err != nil {
		t.Skipf("needs registry:2 and busybox:latest locally: %s", out)
	}
	// One credential saved under another key, which can't be decrypted: the
	// redeploy must go ahead and say so in its output.
	oldKey, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	m.store.SetCipher(oldKey)
	if _, err := m.store.CreateRegistry(ctx, "broken", "quay.io", "x", "y"); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	key[0] = 1
	cph, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	m.store.SetCipher(cph)

	addr, ref, auth := privateRegistry(t, ctx)
	if _, err := m.store.CreateRegistry(ctx, "private", addr, auth.Username, auth.Password); err != nil {
		t.Fatal(err)
	}

	const slug = "dctest-regauth-stack"
	dir := t.TempDir()
	compose := fmt.Sprintf("services:\n  app:\n    image: %s\n    pull_policy: always\n    command: [\"sleep\", \"300\"]\n", ref)
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	freeStack(ctx, m, slug)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ComposeDown(bg, dir, slug, nil)
		freeStack(bg, m, slug)
	})

	// Create the stack with the real CLI so its labels are genuine.
	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{auth})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ComposeUpFiles(ctx, dir, slug, nil, env, nil, false)
	cleanup()
	if err != nil {
		t.Fatalf("could not create the stack: %v\n%s", err, out)
	}

	out, err = m.StackRedeploy(ctx, 0, slug)
	if err != nil {
		t.Fatalf("StackRedeploy didn't use the stored credentials: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Warning: the stored credential for quay.io could not be decrypted") {
		t.Errorf("the skipped credential isn't named in the redeploy output:\n%s", out)
	}
}

// End to end: a wrong credential for the same registry in an inherited
// DOCKER_AUTH_CONFIG must not beat the stored one.
func TestComposeStoredCredentialBeatsDockerAuthConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("docker integration test; skipped under -short")
	}
	ctx := context.Background()
	if !ComposeAvailable(ctx) {
		t.Skip("docker compose not available")
	}
	if out, err := dockerCLI(ctx, nil, "image", "inspect", "registry:2", "busybox:latest"); err != nil {
		t.Skipf("needs registry:2 and busybox:latest locally: %s", out)
	}
	addr, ref, auth := privateRegistry(t, ctx)
	wrong := base64.StdEncoding.EncodeToString([]byte("dctest:wrong-password"))
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{"`+addr+`":{"auth":"`+wrong+`"}}}`)

	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{auth})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	dir := t.TempDir()
	compose := fmt.Sprintf("services:\n  app:\n    image: %s\n", ref)
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("dctest-regauth-env-%d", time.Now().UnixNano())
	if out, err := runComposeFiles(ctx, dir, project, nil, nil, "pull"); err == nil {
		t.Fatalf("the wrong env credential let the pull through, so this test proves nothing: %s", out)
	}
	if out, err := runComposeFiles(ctx, dir, project, env, nil, "pull"); err != nil {
		t.Fatalf("the inherited DOCKER_AUTH_CONFIG beat the stored credential: %v: %s", err, out)
	}
}
