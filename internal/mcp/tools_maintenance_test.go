package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestListMaintenanceWindowsIncludesRecurringSchedule is the fix for a real
// gap: an earlier version of toMaintenanceWindowOut carried only Recurring
// and StartsAt for a recurring window, so a caller had no way to say WHEN it
// actually runs. list_maintenance_windows never creates a recurring window
// itself (that's REST/UI-only), but it must still describe one created
// there.
func TestListMaintenanceWindowsIncludesRecurringSchedule(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	u.Role = "admin"
	p := &principal{user: u}

	if _, err := h.deps.Store.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "weekly", Reason: "r", Recurring: true,
		Weekdays: []time.Weekday{time.Sunday, time.Wednesday}, TimeOfDay: "02:00", DurationMin: 90,
		Timezone: "America/New_York", StartsAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	_, out, err := h.listMaintenanceWindows(ctx, reqFor(p), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Windows) != 1 {
		t.Fatalf("expected one window, got %d", len(out.Windows))
	}
	w := out.Windows[0]
	if len(w.Weekdays) != 2 || w.TimeOfDay != "02:00" || w.DurationMin != 90 || w.Timezone != "America/New_York" {
		t.Fatalf("recurring schedule fields missing from tool output: %+v", w)
	}
}

// TestCreateMaintenanceWindowRejectsUnknownSeverity mirrors the REST-side
// validation — an MCP caller can send scope values the REST form's <select>
// would never produce.
func TestCreateMaintenanceWindowRejectsUnknownSeverity(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	u.Role = "admin"
	p := &principal{user: u}

	if _, _, err := h.createMaintenanceWindow(ctx, reqFor(p), createMaintenanceWindowInput{
		Name: "w", Reason: "r", DurationMin: 30, Severities: []string{"urgent"},
	}); err == nil {
		t.Fatal("an unknown severity should be rejected — it can never match a real event")
	}
}

// TestCreateAndEndMaintenanceWindowNormalizeTheLocalHostAlias is the MCP
// half of the local-host normalization fix: creating a window scoped to the
// local daemon's REAL seeded-row id must be stored under the canonical 0
// alias, and end_maintenance_window must be able to find it again by the
// SAME alias a caller naturally uses (0).
func TestCreateAndEndMaintenanceWindowNormalizeTheLocalHostAlias(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	if err := h.deps.Store.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	hosts, err := h.deps.Store.ListHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("expected the seeded local host, got %d err=%v", len(hosts), err)
	}
	realLocalID := hosts[0].ID
	if realLocalID == 0 {
		t.Fatal("test setup: the seeded local host's real row id should not be 0")
	}

	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	u.Role = "admin"
	p := &principal{user: u}

	_, out, err := h.createMaintenanceWindow(ctx, reqFor(p), createMaintenanceWindowInput{
		Name: "local", Reason: "r", DurationMin: 30, HostIDs: []int64{realLocalID},
	})
	if err != nil {
		t.Fatalf("create with the real local host id: %v", err)
	}
	if len(out.HostIDs) != 1 || out.HostIDs[0] != 0 {
		t.Fatalf("the real local host id should be normalized to the 0 alias, got %v", out.HostIDs)
	}

	if _, _, err := h.endMaintenanceWindow(ctx, reqFor(p), endMaintenanceWindowInput{ID: out.ID}); err != nil {
		t.Fatalf("ending the window should succeed: %v", err)
	}
}
