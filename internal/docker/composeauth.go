package docker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// dockerHubConfigKey is the key the Docker CLI files Docker Hub credentials
// under in config.json. Every other registry is keyed by its bare host.
const dockerHubConfigKey = "https://index.docker.io/v1/"

// ComposeRegistryEnv prepares a `docker compose` run to log in with the
// credentials stored under Registries. Compose reads registry credentials from
// the Docker CLI config, not from the daemon or the API, so without this a
// deploy only had whatever `docker login` the server's OS user had done.
//
// It writes a private, temporary config dir and returns the env that points the
// CLI at it, plus a cleanup that removes it. Call cleanup when the run is over;
// the file holds passwords. With no stored credentials it changes nothing and
// returns no env.
//
// The temporary dir is a copy of the user's own config with the stored
// credentials laid over it, not a blank one:
//   - config.json keeps every other key (proxies, aliases, credsStore…), and
//     auths for registries we don't hold credentials for stay as they were.
//   - every other entry of the source dir (cli-plugins, contexts, buildx) is
//     linked in, so the compose plugin and the current context are still found.
//   - for a registry we hold credentials for, a credential helper the user
//     configured is overridden with an empty helper name, which makes the CLI
//     read the inline auth instead. The stored credential wins on purpose: it
//     is the one managed in the app.
func ComposeRegistryEnv(auths []store.RegistryAuth) (env []string, cleanup func(), err error) {
	noop := func() {}
	if len(auths) == 0 {
		return nil, noop, nil
	}
	src := dockerConfigDir()

	cfg := map[string]json.RawMessage{}
	if src == "" {
		// No home and no DOCKER_CONFIG: start from an empty config rather than
		// resolve "config.json" against the working dir, which is the project's.
	} else if b, err := os.ReadFile(filepath.Join(src, "config.json")); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, noop, fmt.Errorf("read %s: %w", filepath.Join(src, "config.json"), err)
		}
	} else if !os.IsNotExist(err) {
		return nil, noop, err
	}

	authMap := map[string]json.RawMessage{}
	if raw, ok := cfg["auths"]; ok {
		if err := json.Unmarshal(raw, &authMap); err != nil {
			return nil, noop, fmt.Errorf("config.json auths: %w", err)
		}
	}
	helpers := map[string]string{}
	if raw, ok := cfg["credHelpers"]; ok {
		if err := json.Unmarshal(raw, &helpers); err != nil {
			return nil, noop, fmt.Errorf("config.json credHelpers: %w", err)
		}
	}
	for _, a := range auths {
		host := store.NormalizeRegistryHost(a.Address)
		key := host
		if host == "docker.io" {
			key = dockerHubConfigKey
		}
		entry, err := json.Marshal(map[string]string{
			"auth": base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password)),
		})
		if err != nil {
			return nil, noop, err
		}
		authMap[key] = entry
		// The CLI looks a helper up by the key it was asked for. Override both
		// spellings so neither a helper nor a global credsStore takes precedence.
		helpers[key] = ""
		helpers[host] = ""
	}
	if cfg["auths"], err = json.Marshal(authMap); err != nil {
		return nil, noop, err
	}
	if cfg["credHelpers"], err = json.Marshal(helpers); err != nil {
		return nil, noop, err
	}
	out, err := json.MarshalIndent(cfg, "", "\t")
	if err != nil {
		return nil, noop, err
	}

	dir, err := os.MkdirTemp("", "dc-docker-config-*") // 0700
	if err != nil {
		return nil, noop, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) } // removes links, never their targets
	if err := os.WriteFile(filepath.Join(dir, "config.json"), out, 0o600); err != nil {
		cleanup()
		return nil, noop, err
	}
	if entries, err := os.ReadDir(src); src != "" && err == nil {
		for _, e := range entries {
			name := e.Name()
			if name == "config.json" || name == "config.json.lock" {
				continue
			}
			if err := os.Symlink(filepath.Join(src, name), filepath.Join(dir, name)); err != nil {
				cleanup()
				return nil, noop, err
			}
		}
	}
	return []string{"DOCKER_CONFIG=" + dir}, cleanup, nil
}

// dockerConfigDir is where the Docker CLI reads its config from for this
// process: $DOCKER_CONFIG, else ~/.docker.
func dockerConfigDir() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker")
}
