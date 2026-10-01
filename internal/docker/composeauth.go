package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// dockerHubConfigKey is the key the Docker CLI files Docker Hub credentials
// under in config.json. Every other registry is keyed by its bare host.
const dockerHubConfigKey = "https://index.docker.io/v1/"

// dockerAuthConfigEnv is the Docker CLI's in-memory credential source. When set,
// the CLI consults it before config.json and any credential helper.
const dockerAuthConfigEnv = "DOCKER_AUTH_CONFIG"

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
//     read the inline auth instead.
//   - an inherited DOCKER_AUTH_CONFIG, which the CLI consults before all of the
//     above, is passed on with the stored credentials laid over it too.
//
// The stored credential wins on purpose: it is the one managed in the app.
//
// Problems with the user's own config don't stop the deploy, the same as the
// Docker CLI only warns about them. They come back as warnings for the output.
func ComposeRegistryEnv(auths []store.RegistryAuth) (env []string, cleanup func(), warnings []string, err error) {
	noop := func() {}
	if len(auths) == 0 {
		return nil, noop, nil, nil
	}
	src := dockerConfigDir()

	cfg := map[string]json.RawMessage{}
	if src != "" {
		// src == "" (no home, no DOCKER_CONFIG) starts from an empty config rather
		// than resolve "config.json" against the working dir, which is the project's.
		path := filepath.Join(src, "config.json")
		if b, rerr := os.ReadFile(path); rerr == nil {
			if jerr := json.Unmarshal(b, &cfg); jerr != nil {
				warnings = append(warnings, fmt.Sprintf("%s is not valid JSON and was ignored for this deploy: %v", path, jerr))
				cfg = map[string]json.RawMessage{}
			}
		} else if !os.IsNotExist(rerr) {
			warnings = append(warnings, fmt.Sprintf("could not read %s, so it was ignored for this deploy: %v", path, rerr))
		}
	}
	// json.Unmarshal turns a JSON null into a nil map, and a valid config may
	// carry `null` at the top or for either key. Writing into a nil map panics.
	if cfg == nil {
		cfg = map[string]json.RawMessage{}
	}
	authMap := map[string]json.RawMessage{}
	if raw, ok := cfg["auths"]; ok {
		if jerr := json.Unmarshal(raw, &authMap); jerr != nil {
			warnings = append(warnings, fmt.Sprintf("the auths in your Docker config are malformed and were ignored: %v", jerr))
			authMap = map[string]json.RawMessage{}
		}
	}
	if authMap == nil {
		authMap = map[string]json.RawMessage{}
	}
	helpers := map[string]string{}
	if raw, ok := cfg["credHelpers"]; ok {
		if jerr := json.Unmarshal(raw, &helpers); jerr != nil {
			warnings = append(warnings, fmt.Sprintf("the credHelpers in your Docker config are malformed and were ignored: %v", jerr))
			helpers = map[string]string{}
		}
	}
	if helpers == nil {
		helpers = map[string]string{}
	}

	stored := map[string]json.RawMessage{} // config key -> {"auth": …}
	seen := map[string]bool{}
	for _, a := range auths {
		host := store.NormalizeRegistryHost(a.Address)
		if seen[host] {
			continue // auths come oldest first, and the oldest entry wins, as in AuthForHost
		}
		seen[host] = true
		key := host
		if host == "docker.io" {
			key = dockerHubConfigKey
		}
		entry, merr := json.Marshal(map[string]string{
			"auth": base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password)),
		})
		if merr != nil {
			return nil, noop, warnings, merr
		}
		stored[key] = entry
		authMap[key] = entry
		// The CLI looks a helper up by the key it was asked for. Override both
		// spellings so neither a helper nor a global credsStore takes precedence.
		helpers[key] = ""
		helpers[host] = ""
	}
	if cfg["auths"], err = json.Marshal(authMap); err != nil {
		return nil, noop, warnings, err
	}
	if cfg["credHelpers"], err = json.Marshal(helpers); err != nil {
		return nil, noop, warnings, err
	}
	out, err := json.MarshalIndent(cfg, "", "\t")
	if err != nil {
		return nil, noop, warnings, err
	}

	dir, err := os.MkdirTemp("", "dc-docker-config-*") // 0700
	if err != nil {
		return nil, noop, warnings, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) } // removes links, never their targets
	if err := os.WriteFile(filepath.Join(dir, "config.json"), out, 0o600); err != nil {
		cleanup()
		return nil, noop, warnings, err
	}
	if src != "" {
		if entries, rerr := os.ReadDir(src); rerr == nil {
			for _, e := range entries {
				name := e.Name()
				if name == "config.json" || name == "config.json.lock" {
					continue
				}
				// src is absolute (dockerConfigDir), so the link resolves from the
				// temp dir as well; a relative target would point inside it.
				if err := os.Symlink(filepath.Join(src, name), filepath.Join(dir, name)); err != nil {
					cleanup()
					return nil, noop, warnings, err
				}
			}
		}
	}
	env = []string{"DOCKER_CONFIG=" + dir}
	if v, ok := overlayAuthConfigEnv(os.Getenv(dockerAuthConfigEnv), stored); ok {
		env = append(env, dockerAuthConfigEnv+"="+v)
	}
	return env, cleanup, warnings, nil
}

// RegistryEnvFromStore is ComposeRegistryEnv for every credential in st. A
// stored credential that can't be decrypted is left out and named in warnings.
func RegistryEnvFromStore(ctx context.Context, st *store.Store) (env []string, cleanup func(), warnings []string, err error) {
	auths, skipped, err := st.AllRegistryAuths(ctx)
	if err != nil {
		return nil, func() {}, nil, fmt.Errorf("load registry credentials: %w", err)
	}
	for _, addr := range skipped {
		warnings = append(warnings, fmt.Sprintf("the stored credential for %s could not be decrypted and was not used", addr))
	}
	env, cleanup, more, err := ComposeRegistryEnv(auths)
	return env, cleanup, append(warnings, more...), err
}

// warningLines renders warnings as lines heading a command's output.
func warningLines(warnings []string) string {
	var b strings.Builder
	for _, w := range warnings {
		b.WriteString("Warning: " + w + "\n")
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// overlayAuthConfigEnv returns an inherited DOCKER_AUTH_CONFIG with the stored
// credentials laid over it. The CLI reads that variable before config.json, so
// leaving it as it is would let an entry for the same registry beat the stored
// one. ok is false when there is nothing to pass on: the variable is unset, or
// it isn't valid, in which case the CLI ignores it anyway.
func overlayAuthConfigEnv(inherited string, stored map[string]json.RawMessage) (string, bool) {
	if inherited == "" {
		return "", false
	}
	var v struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	// As strict as the CLI (unknown fields rejected), so a value the CLI would
	// have ignored isn't turned into one it uses.
	dec := json.NewDecoder(strings.NewReader(inherited))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if v.Auths == nil {
		v.Auths = map[string]json.RawMessage{}
	}
	for k, entry := range stored {
		v.Auths[k] = entry
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// dockerConfigDir is where the Docker CLI reads its config from for this
// process: $DOCKER_CONFIG, else ~/.docker. It is made absolute, because entries
// of it are linked from another directory.
func dockerConfigDir() string {
	d := os.Getenv("DOCKER_CONFIG")
	if d == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		d = filepath.Join(home, ".docker")
	}
	abs, err := filepath.Abs(d)
	if err != nil {
		return ""
	}
	return abs
}
