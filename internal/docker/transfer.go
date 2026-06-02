package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/pkg/jsonmessage"
)

// SaveImage streams one or more images as a tar archive (docker save format).
// The caller is responsible for closing the returned reader.
func (m *Manager) SaveImage(ctx context.Context, hostID int64, refs []string) (io.ReadCloser, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return cli.ImageSave(ctx, refs)
}

// ExportContainer streams a container's filesystem as a tar archive.
func (m *Manager) ExportContainer(ctx context.Context, hostID int64, id string) (io.ReadCloser, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return cli.ContainerExport(ctx, id)
}

// LoadImage loads images from a tar archive (docker save format) and returns
// the daemon's human-readable summary (e.g. "Loaded image: repo:tag").
func (m *Manager) LoadImage(ctx context.Context, hostID int64, tar io.Reader) (string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", err
	}
	resp, err := cli.ImageLoad(ctx, tar)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return collectStream(resp.Body), nil
}

// ImportImage creates an image from a filesystem tarball (docker import),
// tagging it as ref. It returns the daemon's output summary.
func (m *Manager) ImportImage(ctx context.Context, hostID int64, tarball io.Reader, ref string) (string, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return "", err
	}
	rc, err := cli.ImageImport(ctx, image.ImportSource{Source: tarball, SourceName: "-"}, ref, image.ImportOptions{})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return collectStream(rc), nil
}

// collectStream reads a daemon JSON-message stream and joins its human-readable
// parts (stream text, status, or error) into a single summary string.
func collectStream(r io.Reader) string {
	var b strings.Builder
	dec := json.NewDecoder(r)
	for {
		var jm jsonmessage.JSONMessage
		if err := dec.Decode(&jm); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}
		switch {
		case jm.Error != nil:
			b.WriteString(jm.Error.Message)
		case jm.Stream != "":
			b.WriteString(jm.Stream)
		case jm.Status != "":
			b.WriteString(jm.Status)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}
