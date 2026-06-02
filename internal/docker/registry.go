package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/pkg/jsonmessage"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// registryHost extracts the registry hostname from an image reference. A
// reference is "[host[:port]/]repo[:tag][@digest]"; with no explicit host (or a
// first component that isn't host-like) it defaults to Docker Hub.
func registryHost(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return "docker.io"
	}
	first := ref[:slash]
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return "docker.io"
}

// encodeAuth marshals credentials into the base64 X-Registry-Auth value the
// Docker API expects on pull/push.
func encodeAuth(a *store.RegistryAuth) (string, error) {
	cfg := registry.AuthConfig{
		Username:      a.Username,
		Password:      a.Password,
		ServerAddress: a.Address,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// authForRef resolves stored credentials for an image reference's registry and
// returns the encoded auth header value, or "" when none is configured (so the
// pull proceeds anonymously).
func (m *Manager) authForRef(ctx context.Context, ref string) string {
	auth, err := m.store.AuthForHost(ctx, registryHost(ref))
	if err != nil {
		return ""
	}
	enc, err := encodeAuth(auth)
	if err != nil {
		return ""
	}
	return enc
}

// TagImage adds a new tag (target) to an existing image (source). It is the
// prerequisite for pushing a local image under a registry-qualified name.
func (m *Manager) TagImage(ctx context.Context, hostID int64, source, target string) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	return cli.ImageTag(ctx, source, target)
}

// PushImage pushes a reference to its registry, streaming progress like a pull.
// Pushing requires credentials for the target registry; without them we fail
// early with a clear message rather than letting the daemon return a raw 401.
func (m *Manager) PushImage(ctx context.Context, hostID int64, ref string, onProgress func(PullProgress)) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	host := registryHost(ref)
	auth, err := m.store.AuthForHost(ctx, host)
	if err != nil {
		return fmt.Errorf("no credentials configured for registry %q — add them under Registries", host)
	}
	enc, err := encodeAuth(auth)
	if err != nil {
		return err
	}

	rc, err := cli.ImagePush(ctx, ref, image.PushOptions{RegistryAuth: enc})
	if err != nil {
		return err
	}
	defer rc.Close()
	return streamJSONProgress(rc, onProgress)
}

// RegistryLogin verifies a set of credentials against a registry via the daemon.
func (m *Manager) RegistryLogin(ctx context.Context, hostID int64, a store.RegistryAuth) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	_, err = cli.RegistryLogin(ctx, registry.AuthConfig{
		Username:      a.Username,
		Password:      a.Password,
		ServerAddress: a.Address,
	})
	return err
}

// streamJSONProgress decodes the daemon's newline-delimited JSON progress
// stream (shared by pull and push) into PullProgress callbacks.
func streamJSONProgress(rc io.Reader, onProgress func(PullProgress)) error {
	dec := json.NewDecoder(rc)
	for {
		var jm jsonmessage.JSONMessage
		if err := dec.Decode(&jm); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if jm.Error != nil {
			return errors.New(jm.Error.Message)
		}
		p := PullProgress{Status: jm.Status, ID: jm.ID}
		if jm.Progress != nil {
			p.Current = jm.Progress.Current
			p.Total = jm.Progress.Total
		}
		onProgress(p)
	}
}
