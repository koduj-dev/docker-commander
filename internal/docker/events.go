package docker

import (
	"context"
	"errors"
	"strings"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
)

// Event is a simplified container lifecycle event for the monitor/alert engine.
type Event struct {
	Action        string // "die", "start", "stop", "kill", "oom", "health_status: unhealthy", ...
	ContainerID   string
	ContainerName string
	Image         string
	ExitCode      string
}

// WatchEvents streams container events from the host, invoking fn for each,
// until ctx is cancelled or the stream errors.
func (m *Manager) WatchEvents(ctx context.Context, hostID int64, fn func(Event)) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	f := filters.NewArgs(filters.Arg("type", "container"))
	msgs, errs := cli.Events(ctx, events.ListOptions{Filters: f})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-errs:
			if !ok {
				// The SDK closed the error channel (e.g. daemon restart / stream
				// ended) without a value — surface a real error so the caller
				// logs and reconnects instead of silently backing off.
				return errors.New("docker event stream closed")
			}
			return err
		case msg := <-msgs:
			fn(Event{
				Action:        string(msg.Action),
				ContainerID:   msg.Actor.ID,
				ContainerName: strings.TrimPrefix(msg.Actor.Attributes["name"], "/"),
				Image:         msg.Actor.Attributes["image"],
				ExitCode:      msg.Actor.Attributes["exitCode"],
			})
		}
	}
}
