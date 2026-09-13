package monitor

import (
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// NotifySystem is fireHostAlert's shared plumbing, generalized for other
// ruleless, system-triggered notifications (e.g. an available image update)
// to reuse instead of hand-rolling InsertAlertEvent + a maintenance-window
// check + emailNotify themselves.

func TestNotifySystem_RecordsAnUnsuppressedEvent(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)

	m.NotifySystem(&store.AlertEvent{
		Type: "image_update", Severity: "info", HostID: 1, HostName: "host1",
		Project: "shop", ContainerName: "web", Message: "a newer image is available",
	})

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("events: got %d, err=%v", len(events), err)
	}
	ev := events[0]
	if ev.Suppressed {
		t.Error("no maintenance window exists — the event must not be suppressed")
	}
	if ev.Type != "image_update" || ev.Project != "shop" || ev.ContainerName != "web" {
		t.Errorf("event fields not round-tripped: %+v", ev)
	}
}

// A maintenance window scoped to the event's own project must suppress it —
// proves NotifySystem passes ev.Project/ContainerName through to the window
// lookup, not just HostID (which fireHostAlert's own narrower case never
// exercised, since a host-down event has no project/container to scope by).
func TestNotifySystem_SuppressedByProjectScopedMaintenanceWindow(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	const hostID = int64(1)

	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "deploy window", Reason: "planned", Project: "shop",
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	m.NotifySystem(&store.AlertEvent{
		Type: "image_update", Severity: "info", HostID: hostID,
		Project: "shop", ContainerName: "web", Message: "a newer image is available",
	})

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("events: got %d, err=%v", len(events), err)
	}
	if !events[0].Suppressed {
		t.Errorf("event should be suppressed by the project-scoped window: %+v", events[0])
	}
}
