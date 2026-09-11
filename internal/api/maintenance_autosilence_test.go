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
