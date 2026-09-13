package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/selfupdate"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestGranularityAllows(t *testing.T) {
	cases := []struct {
		policy, delta string
		want          bool
	}{
		{"patch", "patch", true},
		{"patch", "minor", false},
		{"patch", "major", false},
		{"minor", "patch", true},
		{"minor", "minor", true},
		{"minor", "major", false},
		{"major", "patch", true},
		{"major", "minor", true},
		{"major", "major", true},
		{"", "patch", false}, // no policy configured — never auto-apply
	}
	for _, c := range cases {
		if got := granularityAllows(c.policy, c.delta); got != c.want {
			t.Errorf("granularityAllows(%q, %q) = %v, want %v", c.policy, c.delta, got, c.want)
		}
	}
}

// TestShouldAutoApply exercises every gate in isolation, in pure code with no
// I/O, so a removed gate is caught deterministically regardless of whether
// the test sandbox happens to have network access.
func TestShouldAutoApply(t *testing.T) {
	base := func() (updateStatus, store.SelfUpdatePolicy) {
		return updateStatus{Current: "1.0.0", Latest: "1.1.0", UpdateAvailable: true},
			store.SelfUpdatePolicy{Enabled: true, Granularity: "minor"}
	}

	t.Run("all gates pass", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)}
		srv.OnRestart(func() {})
		st, pol := base()
		if !srv.shouldAutoApply(pol, st) {
			t.Error("expected true when every gate passes")
		}
	})
	t.Run("update check disabled", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", false, true)}
		srv.OnRestart(func() {})
		st, pol := base()
		if srv.shouldAutoApply(pol, st) {
			t.Error("must be false when the update check itself is disabled")
		}
	})
	t.Run("self-update disabled", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, false)}
		srv.OnRestart(func() {})
		st, pol := base()
		if srv.shouldAutoApply(pol, st) {
			t.Error("must be false when self-update (web-triggered apply) is disabled")
		}
	})
	t.Run("no restart hook", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)} // OnRestart never called
		st, pol := base()
		if srv.shouldAutoApply(pol, st) {
			t.Error("must be false without a restart hook — auto-apply would swap the binary and never restart")
		}
	})
	t.Run("policy disabled", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)}
		srv.OnRestart(func() {})
		st, pol := base()
		pol.Enabled = false
		if srv.shouldAutoApply(pol, st) {
			t.Error("must be false when the admin has not opted in")
		}
	})
	t.Run("no update available", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)}
		srv.OnRestart(func() {})
		st, pol := base()
		st.UpdateAvailable = false
		if srv.shouldAutoApply(pol, st) {
			t.Error("must be false when no update is available, regardless of policy")
		}
	})
	t.Run("delta above granularity ceiling", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)}
		srv.OnRestart(func() {})
		st, pol := base()
		st.Latest = "2.0.0" // major bump
		pol.Granularity = "patch"
		if srv.shouldAutoApply(pol, st) {
			t.Error("a major bump under a patch-only policy must never auto-apply")
		}
	})
}

// fakeApply is a stand-in for the checker's real apply() so autoApply's
// wiring (does it call applyFn, does it record the result) can be tested
// without ever touching the network.
func fakeApply(result selfupdate.Result, err error) (func(context.Context) (selfupdate.Result, error), *int) {
	calls := 0
	return func(context.Context) (selfupdate.Result, error) {
		calls++
		return result, err
	}, &calls
}

func newAutoApplyTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := &Server{store: st, update: newUpdateChecker("1.0.0", true, true)}
	srv.OnRestart(func() {})
	return srv, st
}

func seedAvailable(u *updateChecker, current, latest string) {
	u.ok, u.at = true, time.Now()
	u.cached = updateStatus{Current: current, Latest: latest, UpdateAvailable: current != latest}
}

func TestAutoApply_GateFailureNeverCallsApplyFn(t *testing.T) {
	cases := []struct {
		name   string
		policy store.SelfUpdatePolicy
	}{
		{"no policy configured", store.SelfUpdatePolicy{}},
		{"policy disabled", store.SelfUpdatePolicy{Enabled: false, Granularity: "major"}},
		{"granularity below delta", store.SelfUpdatePolicy{Enabled: true, Granularity: "patch"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, st := newAutoApplyTestServer(t)
			seedAvailable(srv.update, "1.0.0", "2.0.0") // major bump
			if c.policy.Granularity != "" || c.policy.Enabled {
				if err := st.SetSelfUpdatePolicy(t.Context(), c.policy); err != nil {
					t.Fatal(err)
				}
			}
			fn, calls := fakeApply(selfupdate.Result{}, nil)
			srv.autoApply(t.Context(), fn)
			if *calls != 0 {
				t.Errorf("applyFn must not be called, was called %d time(s)", *calls)
			}
			if got, _ := st.LastAutoUpdate(t.Context()); got != nil {
				t.Errorf("no auto-update should be recorded, got %+v", got)
			}
		})
	}
}

func TestAutoApply_SuccessRecordsUpdateAuditsAndRestarts(t *testing.T) {
	srv, st := newAutoApplyTestServer(t)
	seedAvailable(srv.update, "1.0.0", "1.0.1")
	if err := st.SetSelfUpdatePolicy(t.Context(), store.SelfUpdatePolicy{Enabled: true, Granularity: "patch"}); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	srv.OnRestart(func() { restarted <- struct{}{} })

	fn, calls := fakeApply(selfupdate.Result{From: "1.0.0", To: "1.0.1"}, nil)
	srv.autoApply(t.Context(), fn)

	if *calls != 1 {
		t.Fatalf("applyFn should be called exactly once, got %d", *calls)
	}
	last, err := st.LastAutoUpdate(t.Context())
	if err != nil || last == nil || last.Version != "1.0.1" {
		t.Errorf("expected LastAutoUpdate to record version 1.0.1, got %+v (err=%v)", last, err)
	}
	entries, err := st.RecentAudit(t.Context(), 10, 0)
	if err != nil || len(entries) == 0 || entries[0].Action != "update.apply" || entries[0].Username != "system" {
		t.Errorf("expected a system-attributed update.apply audit entry, got %+v (err=%v)", entries, err)
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Error("expected onRestart to fire after a successful auto-apply")
	}
}

func TestAutoApply_ApplyFnErrorNeverRecordsOrRestarts(t *testing.T) {
	srv, st := newAutoApplyTestServer(t)
	seedAvailable(srv.update, "1.0.0", "1.0.1")
	if err := st.SetSelfUpdatePolicy(t.Context(), store.SelfUpdatePolicy{Enabled: true, Granularity: "patch"}); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	srv.OnRestart(func() { restarted <- struct{}{} })

	fn, calls := fakeApply(selfupdate.Result{}, errors.New("checksum mismatch"))
	srv.autoApply(t.Context(), fn)

	if *calls != 1 {
		t.Fatalf("applyFn should still be called exactly once, got %d", *calls)
	}
	if got, _ := st.LastAutoUpdate(t.Context()); got != nil {
		t.Errorf("a failed apply must not record LastAutoUpdate, got %+v", got)
	}
	select {
	case <-restarted:
		t.Error("a failed apply must never trigger a restart")
	case <-time.After(50 * time.Millisecond):
	}
}
