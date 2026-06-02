package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/pkg/jsonmessage"
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

	resp, err := cli.ImageBuild(ctx, buildContext, build.ImageBuildOptions{
		Tags:       opts.Tags,
		Dockerfile: dockerfile,
		NoCache:    opts.NoCache,
		Remove:     true,
		BuildArgs:  args,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	for {
		var jm jsonmessage.JSONMessage
		if err := dec.Decode(&jm); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if jm.Error != nil {
			onMsg(BuildMessage{Error: jm.Error.Message})
			return errors.New(jm.Error.Message)
		}
		if jm.Stream != "" {
			onMsg(BuildMessage{Stream: jm.Stream})
		}
	}
}
