package docker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
)

// DiffEntry is one filesystem change in a container relative to its image.
type DiffEntry struct {
	Kind string `json:"kind"` // modified | added | deleted
	Path string `json:"path"`
}

// TopResult is the process listing inside a container (docker top).
type TopResult struct {
	Titles    []string   `json:"titles"`
	Processes [][]string `json:"processes"`
}

// HistoryEntry is one layer in an image's build history.
type HistoryEntry struct {
	ID        string   `json:"id"`
	Created   int64    `json:"created"`
	CreatedBy string   `json:"createdBy"`
	Size      int64    `json:"size"`
	Comment   string   `json:"comment"`
	Tags      []string `json:"tags"`
}

// UsageCategory is a count + total size for one class of Docker object.
type UsageCategory struct {
	Count int   `json:"count"`
	Size  int64 `json:"size"`
}

// DiskUsage summarises what Docker is storing (docker system df).
type DiskUsage struct {
	LayersSize int64         `json:"layersSize"`
	Images     UsageCategory `json:"images"`
	Containers UsageCategory `json:"containers"`
	Volumes    UsageCategory `json:"volumes"`
	BuildCache UsageCategory `json:"buildCache"`
}

// EventMsg is one Docker daemon event, flattened for the UI.
type EventMsg struct {
	Time   int64             `json:"time"`
	Type   string            `json:"type"`   // container | image | network | volume | …
	Action string            `json:"action"` // start | die | pull | create | …
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Attr   map[string]string `json:"attr,omitempty"`
}

// InspectRaw returns the daemon's raw JSON for an object, preserving every
// field (more faithful than re-marshalling the SDK struct). kind is one of
// container, image, network, volume.
func (m *Manager) InspectRaw(ctx context.Context, hostID int64, kind, id string) (json.RawMessage, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "container":
		_, raw, err := cli.ContainerInspectWithRaw(ctx, id, false)
		return raw, err
	case "image":
		_, raw, err := cli.ImageInspectWithRaw(ctx, id)
		return raw, err
	case "network":
		n, err := cli.NetworkInspect(ctx, id, network.InspectOptions{})
		if err != nil {
			return nil, err
		}
		return json.Marshal(n)
	case "volume":
		v, err := cli.VolumeInspect(ctx, id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	default:
		return nil, fmt.Errorf("unknown inspect kind %q", kind)
	}
}

// ContainerDiff lists filesystem changes since the container started.
func (m *Manager) ContainerDiff(ctx context.Context, hostID int64, id string) ([]DiffEntry, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	changes, err := cli.ContainerDiff(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]DiffEntry, 0, len(changes))
	for _, c := range changes {
		out = append(out, DiffEntry{Kind: changeKind(c.Kind), Path: c.Path})
	}
	return out, nil
}

// changeKind maps Docker's numeric change type to a readable word.
func changeKind(k container.ChangeType) string {
	switch k {
	case container.ChangeModify:
		return "modified"
	case container.ChangeAdd:
		return "added"
	case container.ChangeDelete:
		return "deleted"
	default:
		return "unknown"
	}
}

// ContainerTop returns the processes running inside a container.
func (m *Manager) ContainerTop(ctx context.Context, hostID int64, id string) (*TopResult, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	t, err := cli.ContainerTop(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	return &TopResult{Titles: t.Titles, Processes: t.Processes}, nil
}

// ImageHistory returns the layer history of an image.
func (m *Manager) ImageHistory(ctx context.Context, hostID int64, ref string) ([]HistoryEntry, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	hist, err := cli.ImageHistory(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(hist))
	for _, h := range hist {
		out = append(out, HistoryEntry{
			ID: h.ID, Created: h.Created, CreatedBy: h.CreatedBy,
			Size: h.Size, Comment: h.Comment, Tags: h.Tags,
		})
	}
	return out, nil
}

// DiskUsage reports how much disk Docker objects occupy (docker system df).
func (m *Manager) DiskUsage(ctx context.Context, hostID int64) (*DiskUsage, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	du, err := cli.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return nil, err
	}
	out := &DiskUsage{LayersSize: du.LayersSize}
	out.Images.Count = len(du.Images)
	for _, im := range du.Images {
		if im.Size > 0 {
			out.Images.Size += im.Size
		}
	}
	out.Containers.Count = len(du.Containers)
	for _, c := range du.Containers {
		out.Containers.Size += c.SizeRw
	}
	out.Volumes.Count = len(du.Volumes)
	for _, v := range du.Volumes {
		if v != nil && v.UsageData != nil && v.UsageData.Size > 0 {
			out.Volumes.Size += v.UsageData.Size
		}
	}
	out.BuildCache.Count = len(du.BuildCache)
	for _, bc := range du.BuildCache {
		if bc != nil {
			out.BuildCache.Size += bc.Size
		}
	}
	return out, nil
}

// StreamEvents forwards live daemon events to onEvent until the context is
// cancelled or the daemon stream errors. It mirrors the pull/exec streaming
// pattern so the handler can bridge it straight to a WebSocket.
func (m *Manager) StreamEvents(ctx context.Context, hostID int64, onEvent func(EventMsg)) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	msgs, errs := cli.Events(ctx, events.ListOptions{})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-msgs:
			onEvent(flattenEvent(e))
		case err := <-errs:
			return err
		}
	}
}

// flattenEvent reshapes a daemon event into the UI-facing EventMsg, pulling the
// human-friendly name out of the actor attributes when present.
func flattenEvent(e events.Message) EventMsg {
	name := e.Actor.Attributes["name"]
	if name == "" {
		name = e.Actor.Attributes["image"]
	}
	return EventMsg{
		Time:   e.Time,
		Type:   string(e.Type),
		Action: string(e.Action),
		ID:     e.Actor.ID,
		Name:   name,
		Attr:   e.Actor.Attributes,
	}
}
