package docker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// configDirFromEnv returns the dir a DOCKER_CONFIG= env entry points at.
func configDirFromEnv(t *testing.T, env []string) string {
	t.Helper()
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "DOCKER_CONFIG="); ok {
			return v
		}
	}
	t.Fatalf("no DOCKER_CONFIG entry in %v", env)
	return ""
}

type cliConfig struct {
	Auths       map[string]map[string]string `json:"auths"`
	CredHelpers map[string]string            `json:"credHelpers"`
	CredsStore  string                       `json:"credsStore"`
	Proxies     json.RawMessage              `json:"proxies"`
}

func readCLIConfig(t *testing.T, dir string) cliConfig {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c cliConfig
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// With nothing stored, compose runs exactly as before: no env, no temp dir.
func TestComposeRegistryEnvWithNoCredentialsChangesNothing(t *testing.T) {
	env, cleanup, _, err := ComposeRegistryEnv(nil)
	defer cleanup()
	if err != nil || env != nil {
		t.Fatalf("want no env and no error, got %v, %v", env, err)
	}
}

// The stored credentials are laid over the user's own config: unrelated keys,
// other registries' auths and other helpers survive; for the registries we hold
// credentials for, the inline auth wins over a helper or a global credsStore.
func TestComposeRegistryEnvMergesOverTheUsersConfig(t *testing.T) {
	src := t.TempDir()
	t.Setenv("DOCKER_CONFIG", src)
	user := `{
		"auths": {"quay.io": {"auth": "dXNlcjpwYXNz"}, "ghcr.io": {"auth": "b2xkOm9sZA=="}},
		"credsStore": "desktop",
		"credHelpers": {"ghcr.io": "gh", "gcr.io": "gcloud"},
		"proxies": {"default": {"httpProxy": "http://proxy:3128"}}
	}`
	if err := os.WriteFile(filepath.Join(src, "config.json"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}

	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{
		{Address: "ghcr.io", Username: "bot", Password: "s3cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := readCLIConfig(t, configDirFromEnv(t, env))

	if got := c.Auths["ghcr.io"]["auth"]; got != basicAuth("bot", "s3cret") {
		t.Errorf("ghcr.io auth = %q, want the stored credential", got)
	}
	if got := c.Auths["quay.io"]["auth"]; got != "dXNlcjpwYXNz" {
		t.Errorf("an unrelated registry's auth was lost: %q", got)
	}
	if h, ok := c.CredHelpers["ghcr.io"]; !ok || h != "" {
		t.Errorf("the user's helper for ghcr.io must be overridden with an empty one, got %q (present %v)", h, ok)
	}
	if c.CredHelpers["gcr.io"] != "gcloud" {
		t.Errorf("an unrelated credential helper was lost: %v", c.CredHelpers)
	}
	if c.CredsStore != "desktop" || !strings.Contains(string(c.Proxies), "proxy:3128") {
		t.Errorf("unrelated config keys were lost: credsStore=%q proxies=%s", c.CredsStore, c.Proxies)
	}
	// The user's own file is untouched.
	if b, _ := os.ReadFile(filepath.Join(src, "config.json")); string(b) != user {
		t.Error("the user's config.json was modified")
	}
}

// Docker Hub is filed under the CLI's own key, whatever alias it was stored as.
func TestComposeRegistryEnvFilesDockerHubUnderTheCLIKey(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{
		{Address: "index.docker.io", Username: "me", Password: "tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := readCLIConfig(t, configDirFromEnv(t, env))
	if got := c.Auths["https://index.docker.io/v1/"]["auth"]; got != basicAuth("me", "tok") {
		t.Fatalf("Docker Hub credential not under the CLI key: %v", c.Auths)
	}
}

// PENTEST: the file holds passwords. It must be private, and gone after cleanup.
func TestPentestComposeRegistryConfigIsPrivateAndRemoved(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{
		{Address: "registry.example.com", Username: "u", Password: "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := configDirFromEnv(t, env)
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("SECURITY: temp config dir mode = %v (err %v), want 0700", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("SECURITY: config.json mode = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("SECURITY: the config with passwords survived cleanup")
	}
}

// The rest of the user's config dir (the compose plugin, contexts) stays
// reachable, and cleanup removes only the links, never what they point at.
func TestComposeRegistryEnvKeepsPluginsAndCleansOnlyItsOwnDir(t *testing.T) {
	src := t.TempDir()
	t.Setenv("DOCKER_CONFIG", src)
	plugin := filepath.Join(src, "cli-plugins", "docker-compose")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{{Address: "ghcr.io", Username: "u", Password: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := configDirFromEnv(t, env)
	if _, err := os.Stat(filepath.Join(dir, "cli-plugins", "docker-compose")); err != nil {
		t.Fatalf("the compose plugin is not reachable through the temp config: %v", err)
	}
	cleanup()
	if _, err := os.Stat(plugin); err != nil {
		t.Fatalf("cleanup removed the user's own plugin: %v", err)
	}
}

// PENTEST: with no DOCKER_CONFIG and no home, the config must not be read from
// the working directory, which during a deploy is the project's own folder: a
// config.json committed to a project would otherwise steer the CLI.
func TestPentestComposeRegistryEnvIgnoresAConfigInTheWorkingDir(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "config.json"),
		[]byte(`{"proxies":{"default":{"httpProxy":"http://attacker:1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("HOME", "")

	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{{Address: "ghcr.io", Username: "u", Password: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(filepath.Join(configDirFromEnv(t, env), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "attacker") {
		t.Fatal("SECURITY: a config.json from the working directory was merged in")
	}
}

// A user config that isn't valid JSON doesn't stop the deploy: the Docker CLI
// only warns about it, and so do we. The stored credential is still used.
func TestComposeRegistryEnvWarnsAboutACorruptConfigAndCarriesOn(t *testing.T) {
	src := t.TempDir()
	t.Setenv("DOCKER_CONFIG", src)
	if err := os.WriteFile(filepath.Join(src, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, cleanup, warnings, err := ComposeRegistryEnv([]store.RegistryAuth{{Address: "ghcr.io", Username: "u", Password: "p"}})
	if err != nil {
		t.Fatalf("a corrupt user config failed the run: %v", err)
	}
	defer cleanup()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not valid JSON") {
		t.Fatalf("want one warning about the invalid JSON, got %v", warnings)
	}
	if got := readCLIConfig(t, configDirFromEnv(t, env)).Auths["ghcr.io"]["auth"]; got != basicAuth("u", "p") {
		t.Fatalf("the stored credential is missing: %q", got)
	}
}

// A redeploy on an SSH host says, in its own output, that the host's own login
// is used: that is where an "unauthorized" pull would be read. It says so on
// failure too, which is when it matters.
func TestSSHStackRedeployExplainsWhichLoginIsUsed(t *testing.T) {
	var ran string
	run := func(cmd string) (string, error) { ran = cmd; return "pull access denied", errors.New("exit 1") }

	out, err := sshStackRedeploy(run, "/srv/app", "app", "/srv/app/compose.yml")
	if err == nil {
		t.Fatal("the run's error was swallowed")
	}
	if !strings.HasPrefix(out, sshRegistryNote) || !strings.Contains(out, "pull access denied") {
		t.Fatalf("output must start with the registry note and keep the CLI output, got %q", out)
	}
	if !strings.Contains(ran, "docker compose -p 'app' -f '/srv/app/compose.yml' up -d --build") {
		t.Fatalf("unexpected remote command: %q", ran)
	}
}

// A config may hold JSON null at the top or for auths/credHelpers. That must
// not crash the deploy.
func TestComposeRegistryEnvToleratesNullsInTheUsersConfig(t *testing.T) {
	for _, cfg := range []string{`null`, `{"auths": null}`, `{"credHelpers": null}`, `{"auths": null, "credHelpers": null}`} {
		t.Run(cfg, func(t *testing.T) {
			src := t.TempDir()
			t.Setenv("DOCKER_CONFIG", src)
			if err := os.WriteFile(filepath.Join(src, "config.json"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{{Address: "ghcr.io", Username: "u", Password: "p"}})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if got := readCLIConfig(t, configDirFromEnv(t, env)).Auths["ghcr.io"]["auth"]; got != basicAuth("u", "p") {
				t.Fatalf("credential missing after a %s config: %q", cfg, got)
			}
		})
	}
}

// Two stored entries for one registry: the oldest is used, the same one the
// Images page uses (AuthForHost), so deploys and pulls agree.
func TestComposeRegistryEnvUsesTheOldestEntryForARegistry(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{
		{Address: "ghcr.io", Username: "first", Password: "1"},
		{Address: "ghcr.io", Username: "second", Password: "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := readCLIConfig(t, configDirFromEnv(t, env)).Auths["ghcr.io"]["auth"]; got != basicAuth("first", "1") {
		t.Fatalf("want the oldest entry, got %q", got)
	}
}

// A relative DOCKER_CONFIG still yields working links: the plugin is reachable
// through the temp dir, not resolved against it.
func TestComposeRegistryEnvLinksWorkWithARelativeDockerConfig(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	plugin := filepath.Join(base, "relative", "cli-plugins", "docker-compose")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", "relative")

	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{{Address: "ghcr.io", Username: "u", Password: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(configDirFromEnv(t, env), "cli-plugins", "docker-compose")); err != nil {
		t.Fatalf("the plugin isn't reachable through a relative DOCKER_CONFIG: %v", err)
	}
}

// An inherited DOCKER_AUTH_CONFIG is read by the CLI before config.json. It is
// passed on with the stored credential laid over it, and other registries kept.
func TestComposeRegistryEnvOverridesAnInheritedDockerAuthConfig(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{"ghcr.io":{"auth":"`+basicAuth("wrong", "wrong")+`"},"quay.io":{"auth":"`+basicAuth("q", "q")+`"}}}`)

	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{{Address: "ghcr.io", Username: "bot", Password: "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var v string
	for _, e := range env {
		if s, ok := strings.CutPrefix(e, "DOCKER_AUTH_CONFIG="); ok {
			v = s
		}
	}
	var got struct {
		Auths map[string]map[string]string `json:"auths"`
	}
	if err := json.Unmarshal([]byte(v), &got); err != nil {
		t.Fatalf("no usable DOCKER_AUTH_CONFIG passed on (%q): %v", v, err)
	}
	if got.Auths["ghcr.io"]["auth"] != basicAuth("bot", "s3cret") {
		t.Errorf("the inherited entry still wins for ghcr.io: %v", got.Auths["ghcr.io"])
	}
	if got.Auths["quay.io"]["auth"] != basicAuth("q", "q") {
		t.Errorf("an unrelated inherited entry was lost: %v", got.Auths)
	}
}

// A DOCKER_AUTH_CONFIG the CLI would reject (unknown field) is not rewritten
// into one it accepts.
func TestComposeRegistryEnvLeavesAnInvalidDockerAuthConfigAlone(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{},"credsStore":"x"}`)
	env, cleanup, _, err := ComposeRegistryEnv([]store.RegistryAuth{{Address: "ghcr.io", Username: "u", Password: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, e := range env {
		if strings.HasPrefix(e, "DOCKER_AUTH_CONFIG=") {
			t.Fatalf("an invalid DOCKER_AUTH_CONFIG was turned into a valid one: %s", e)
		}
	}
}
