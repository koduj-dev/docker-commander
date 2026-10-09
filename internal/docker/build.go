package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// BuildMessage is one line of build output forwarded to the UI. Build streams
// are mostly free-text (Stream); Error carries a build failure.
type BuildMessage struct {
	Stream string `json:"stream,omitempty"`
	Error  string `json:"error,omitempty"`
}

// BuildOptions are the user-facing knobs for an image build.
type BuildOptions struct {
	Tags       []string
	Dockerfile string
	NoCache    bool
	BuildArgs  map[string]string
	// Registries are the registry hosts the Dockerfile pulls from (see
	// BuildRegistries). Only their stored credentials are sent; none at all
	// when it is empty.
	Registries []string
	// Warnings are shown with the build's own warnings, once it is over.
	Warnings []string
}

// BuildImage builds an image from a tar (optionally gzip'd) build context,
// streaming the daemon's output line by line. The context reader is supplied by
// the caller (typically the uploaded request body).
func (m *Manager) BuildImage(ctx context.Context, hostID int64, buildContext io.Reader, opts BuildOptions, onMsg func(BuildMessage)) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}

	args := make(map[string]*string, len(opts.BuildArgs))
	for k, v := range opts.BuildArgs {
		val := v
		args[k] = &val
	}
	dockerfile := opts.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	// A private base image (FROM ghcr.io/…) is pulled by the daemon during the
	// build, with the credentials sent along here. Only the registries the
	// Dockerfile uses: the daemon receives the whole set in one header, so on a
	// remote host every other stored credential would have gone to it too.
	auths, skipped, err := m.store.AllRegistryAuths(ctx)
	if err != nil {
		return fmt.Errorf("load registry credentials: %w", err)
	}
	// Said once the build is over, never before it starts. buildContext is
	// usually the request body, and the first message sends the response
	// headers: net/http then drains or cuts off a body the handler hasn't read,
	// and the daemon would get an empty or truncated context.
	warned := false
	warn := func() {
		if warned {
			return
		}
		warned = true
		for _, w := range opts.Warnings {
			onMsg(BuildMessage{Stream: "Warning: " + w + "\n"})
		}
		inUse := map[string]bool{}
		for _, h := range opts.Registries {
			inUse[store.NormalizeRegistryHost(h)] = true
		}
		for _, addr := range skipped {
			if !inUse[store.NormalizeRegistryHost(addr)] {
				continue // a registry this build doesn't use: not its business
			}
			onMsg(BuildMessage{Stream: "Warning: the stored credential for " + addr + " could not be decrypted and was not used\n"})
		}
	}
	defer warn()

	resp, err := cli.ImageBuild(ctx, buildContext, client.ImageBuildOptions{
		Tags:        opts.Tags,
		Dockerfile:  dockerfile,
		NoCache:     opts.NoCache,
		Remove:      true,
		BuildArgs:   args,
		AuthConfigs: buildAuthConfigs(auths, opts.Registries),
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	for {
		var jm jsonstream.Message
		if err := dec.Decode(&jm); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if jm.Error != nil {
			warn() // before the failure, which may well be the missing credential
			onMsg(BuildMessage{Error: jm.Error.Message})
			return errors.New(jm.Error.Message)
		}
		if jm.Stream != "" {
			onMsg(BuildMessage{Stream: jm.Stream})
		}
	}
}

// buildAuthConfigs keys stored credentials the way the daemon looks them up for
// a build: Docker Hub under its index URL, every other registry by its host.
// Only the registries in use are included. auths come oldest first, and the
// oldest entry for a registry wins, as for pulls (AuthForHost) and deploys
// (ComposeRegistryEnv).
func buildAuthConfigs(auths []store.RegistryAuth, inUse []string) map[string]registry.AuthConfig {
	want := make(map[string]bool, len(inUse))
	for _, h := range inUse {
		want[store.NormalizeRegistryHost(h)] = true
	}
	out := make(map[string]registry.AuthConfig, len(inUse))
	for _, a := range auths {
		host := store.NormalizeRegistryHost(a.Address)
		if !want[host] {
			continue
		}
		key := host
		if host == "docker.io" {
			key = dockerHubConfigKey
		}
		if _, seen := out[key]; seen {
			continue
		}
		out[key] = registry.AuthConfig{Username: a.Username, Password: a.Password, ServerAddress: key}
	}
	return out
}
