package api

import (
	"context"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestAutoSilenceForDeployCreatesWindow is the point of the deploy hook: a
// successful deploy should silence alert delivery for that project's own
// host+stack for the configured grace period, without anyone having to
// create a maintenance window by hand.
func TestAutoSilenceForDeployCreatesWindow(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{store: st, cfg: config.Config{DeploySilenceGrace: 5 * time.Minute}}
	ctx := context.Background()

	srv.autoSilenceForDeploy(ctx, &store.Project{Name: "shop", Slug: "shop-prod", HostID: 3})

	windows, err := st.ListMaintenanceWindows(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("expected exactly one auto-silence window, got %d err=%v", len(windows), err)
	}
	w := windows[0]
	if len(w.HostIDs) != 1 || w.HostIDs[0] != 3 {
		t.Errorf("window should be scoped to the deploy's host: %+v", w.HostIDs)
	}
	if w.Project != "shop-prod" {
		t.Errorf("window should be scoped to the deploy's project: %q", w.Project)
	}
	if !w.Active(time.Now()) {
		t.Error("the window should be active immediately after a deploy")
	}
	if w.Active(time.Now().Add(6 * time.Minute)) {
		t.Error("the window should expire after the configured grace period")
	}
}

// TestAutoSilenceForDeployDisabledWhenGraceIsZero confirms 0 really means
// off, not "use some default" — the config doc promises this explicitly.
func TestAutoSilenceForDeployDisabledWhenGraceIsZero(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{store: st, cfg: config.Config{DeploySilenceGrace: 0}}
	ctx := context.Background()

	srv.autoSilenceForDeploy(ctx, &store.Project{Name: "shop", Slug: "shop-prod", HostID: 3})

	windows, err := st.ListMaintenanceWindows(ctx)
	if err != nil || len(windows) != 0 {
		t.Fatalf("expected no auto-silence window with grace=0, got %d err=%v", len(windows), err)
	}
}

// TestAutoSilenceForDeploySuppressesALocalHostAlert is the fix for a real
// bug: a LOCAL project's HostID is 0 (the alias convention the rest of the
// app uses), but the monitor emits alert events carrying the local daemon's
// REAL seeded-row id (whatever autoincrement gave it — commonly 1). Without
// normalizing at write time, the auto-silence window this function creates
// for a local project's deploy would never match a real local-host alert,
// silently defeating the whole feature for what is likely the most common
// case: a single-host install.
func TestAutoSilenceForDeploySuppressesALocalHostAlert(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	hosts, err := st.ListHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("expected the seeded local host, got %d err=%v", len(hosts), err)
	}
	realLocalID := hosts[0].ID
	if realLocalID == 0 {
		t.Fatal("test setup: the seeded local host's real row id should not be 0")
	}

	srv := &Server{store: st, cfg: config.Config{DeploySilenceGrace: 5 * time.Minute}}
	srv.autoSilenceForDeploy(ctx, &store.Project{Name: "shop", Slug: "shop-prod", HostID: 0})

	win, err := st.FindActiveMaintenanceWindow(ctx, realLocalID, "shop-prod", "shop-web-1", 0, "critical", time.Now())
	if err != nil || win == nil {
		t.Fatalf("a local project's auto-silence window should suppress a real local-host alert: win=%v err=%v", win, err)
	}
}

// TestAutoSilenceForDeploySuppressesAnAlertDuringGrace is the integration
// proof that the created window actually does what it's for: an alert
// firing for the deployed project's host during the grace period gets no
// delivery, exactly like a hand-created window would.
func TestAutoSilenceForDeploySuppressesAnAlertDuringGrace(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{store: st, cfg: config.Config{DeploySilenceGrace: 5 * time.Minute}}
	ctx := context.Background()

	srv.autoSilenceForDeploy(ctx, &store.Project{Name: "shop", Slug: "shop-prod", HostID: 3})

	win, err := st.FindActiveMaintenanceWindow(ctx, 3, "shop-prod", "shop-web-1", 0, "critical", time.Now())
	if err != nil || win == nil {
		t.Fatalf("an alert on the just-deployed project's host should be suppressed: win=%v err=%v", win, err)
	}
}
