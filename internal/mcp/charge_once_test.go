package mcp

import (
	"context"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// One write operation spends one unit of the control rate limit, however many
// scopes it checks. acknowledge_alert checks the alerts section, then the
// alert's own host; deploy_project and down_project check projects, then the
// project's host. Each used to spend a unit per check, so the ceiling of 30
// changes a minute allowed only 15 of them.

// remaining counts the units left, spending them all.
func remaining(h *handler, uid int64) int {
	n := 0
	for {
		if ok, _ := h.limiter.allow(uid); !ok {
			return n
		}
		n++
	}
}

func TestAcknowledgeAlertSpendsOneUnit(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	h.limiter.burst = 5
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	id, err := h.deps.Store.InsertAlertEvent(ctx, &store.AlertEvent{
		RuleName: "r", Type: "resource", Severity: "warning", HostID: 7, Message: "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.acknowledgeAlert(ctx, reqFor(&principal{user: u}), ackAlertInput{ID: id}); err != nil {
		t.Fatal(err)
	}
	if n := remaining(h, uid); n != 4 {
		t.Errorf("%d of 5 units left after one acknowledgement, want 4", n)
	}
}

func TestDeployProjectSpendsOneUnit(t *testing.T) {
	for _, tc := range []struct {
		name string
		host int64
	}{{"local", 0}, {"remote", 7}} {
		t.Run(tc.name, func(t *testing.T) {
			h, uid := newTestHandler(t, nil)
			h.limiter.burst = 5
			h.deps.DeployProject = func(context.Context, int64, []string, bool) (string, error) { return "ok", nil }
			ctx := context.Background()
			u, err := h.deps.Store.UserByID(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := h.deps.Store.CreateProject(ctx, &store.Project{Name: "p", Slug: "p", ComposeFile: "compose.yml", HostID: tc.host})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := h.deployProject(ctx, reqFor(&principal{user: u}), projectInput{ProjectID: pid}); err != nil {
				t.Fatal(err)
			}
			if n := remaining(h, uid); n != 4 {
				t.Errorf("%d of 5 units left after one deploy, want 4", n)
			}
		})
	}
}

// recheck must stay a check: charging nothing must not mean checking nothing.
func TestRecheckStillRefuses(t *testing.T) {
	h, uid := newTestHandler(t, func(context.Context, *store.User, string, bool, int64) error { return errOutOfScope })
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.recheck(ctx, reqFor(&principal{user: u}), "alerts", true, 3); err == nil {
		t.Fatal("SECURITY: recheck let through a call the access check refused")
	}
}
