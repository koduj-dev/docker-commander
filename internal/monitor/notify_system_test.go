package monitor

import (
	"bytes"
	"log"
	"strings"
	"sync"
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

	if err := m.NotifySystem(&store.AlertEvent{
		Type: "image_update", Severity: "info", HostID: 1, HostName: "host1",
		Project: "shop", ContainerName: "web", Message: "a newer image is available",
	}); err != nil {
		t.Fatalf("NotifySystem: %v", err)
	}

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

	if err := m.NotifySystem(&store.AlertEvent{
		Type: "image_update", Severity: "info", HostID: hostID,
		Project: "shop", ContainerName: "web", Message: "a newer image is available",
	}); err != nil {
		t.Fatalf("NotifySystem: %v", err)
	}

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("events: got %d, err=%v", len(events), err)
	}
	if !events[0].Suppressed {
		t.Errorf("event should be suppressed by the project-scoped window: %+v", events[0])
	}
}

// A caller (e.g. the image-update poller) keys its own idempotency state off
// whether NotifySystem succeeded — it must return a non-nil error when the
// event was never actually recorded, so that state is never advanced for a
// notification nobody will ever see in the feed.
func TestNotifySystem_ReturnsErrorWhenInsertFails(t *testing.T) {
	m, st, _ := newMaintenanceMonitor(t)
	_ = st.Close() // force InsertAlertEvent to fail

	if err := m.NotifySystem(&store.AlertEvent{Type: "image_update", Severity: "info"}); err == nil {
		t.Error("expected an error when the event can't be recorded, got nil")
	}
}

// Before this fix, an InsertAlertEvent failure made NotifySystem return
// immediately without ever attempting delivery — silently dropping a
// critical, unsuppressed alert (e.g. "host is unreachable") on a transient DB
// hiccup. Delivery must still be attempted so the primary message has a
// chance to reach someone, even though bookkeeping (and thus dedup/retry
// state keyed by ev.ID) didn't happen.
func TestNotifySystem_StillAttemptsDeliveryWhenInsertFails(t *testing.T) {
	m, st, _ := newMaintenanceMonitor(t)
	_ = st.Close() // force InsertAlertEvent (and GetSMTP) to fail

	var buf bytes.Buffer
	var mu sync.Mutex
	log.SetOutput(&syncWriter{mu: &mu, w: &buf})
	t.Cleanup(func() { log.SetOutput(logDefaultOutput) })

	if err := m.NotifySystem(&store.AlertEvent{Type: "image_update", Severity: "info"}); err == nil {
		t.Fatal("expected an error when the event can't be recorded, got nil")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := buf.String()
		mu.Unlock()
		if strings.Contains(got, "monitor: smtp config:") {
			return // attemptEmail ran despite the insert failure
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery was never attempted after an insert failure; log output: %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var logDefaultOutput = log.Writer()

type syncWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
