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

// TestAutoApplyEnabled exercises every non-network gate in isolation, in pure
// code, so a removed gate is caught deterministically regardless of whether
// the test sandbox happens to have network access.
func TestAutoApplyEnabled(t *testing.T) {
	t.Run("all gates pass", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)}
		srv.OnRestart(func() {})
		if !srv.autoApplyEnabled(store.SelfUpdatePolicy{Enabled: true, Granularity: "minor"}) {
			t.Error("expected true when every gate passes")
		}
	})
	t.Run("update check disabled", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", false, true)}
		srv.OnRestart(func() {})
		if srv.autoApplyEnabled(store.SelfUpdatePolicy{Enabled: true, Granularity: "minor"}) {
			t.Error("must be false when the update check itself is disabled")
		}
	})
	t.Run("self-update disabled", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, false)}
		srv.OnRestart(func() {})
		if srv.autoApplyEnabled(store.SelfUpdatePolicy{Enabled: true, Granularity: "minor"}) {
			t.Error("must be false when self-update (web-triggered apply) is disabled")
		}
	})
	t.Run("no restart hook", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)} // OnRestart never called
		if srv.autoApplyEnabled(store.SelfUpdatePolicy{Enabled: true, Granularity: "minor"}) {
			t.Error("must be false without a restart hook — auto-apply would swap the binary and never restart")
		}
	})
	t.Run("policy disabled", func(t *testing.T) {
		srv := &Server{update: newUpdateChecker("1.0.0", true, true)}
		srv.OnRestart(func() {})
		if srv.autoApplyEnabled(store.SelfUpdatePolicy{Enabled: false, Granularity: "minor"}) {
			t.Error("must be false when the admin has not opted in")
		}
	})
}

// fakeApplyFn stands in for the checker's real applyIfPolicyAllows so
// autoApply's wiring (does it call applyFn with a predicate reflecting the
// stored policy, does it record the result) can be tested without ever
// touching the network. It invokes the predicate against latestTag exactly
// as the real ApplyChecked would, so a test can assert the predicate makes
// the right call for a given (policy, resolved-tag) pair.
func fakeApplyFn(latestTag string, result selfupdate.Result, err error) (func(context.Context, func(string) bool) (selfupdate.Result, error), *int) {
	calls := 0
	return func(_ context.Context, allowed func(string) bool) (selfupdate.Result, error) {
		calls++
		if !allowed(latestTag) {
			return selfupdate.Result{}, selfupdate.ErrPolicyRefused
		}
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

func TestAutoApply_GateFailureNeverCallsApplyFn(t *testing.T) {
	cases := []struct {
		name   string
		policy store.SelfUpdatePolicy
	}{
		{"no policy configured", store.SelfUpdatePolicy{}},
		{"policy disabled", store.SelfUpdatePolicy{Enabled: false, Granularity: "major"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, st := newAutoApplyTestServer(t)
			if c.policy.Granularity != "" || c.policy.Enabled {
				if err := st.SetSelfUpdatePolicy(t.Context(), c.policy); err != nil {
					t.Fatal(err)
				}
			}
			fn, calls := fakeApplyFn("2.0.0", selfupdate.Result{}, nil)
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

// This is the exact TOCTOU scenario a stale-status-based gate would miss: the
// policy only allows patch releases, and the release actually resolved at
// apply time (simulated by fakeApplyFn) is a major bump — the predicate
// autoApply builds must refuse it regardless of what any earlier check saw.
func TestAutoApply_GranularityCheckedAgainstResolvedRelease(t *testing.T) {
	srv, st := newAutoApplyTestServer(t)
	if err := st.SetSelfUpdatePolicy(t.Context(), store.SelfUpdatePolicy{Enabled: true, Granularity: "patch"}); err != nil {
		t.Fatal(err)
	}
	fn, calls := fakeApplyFn("2.0.0", selfupdate.Result{From: "1.0.0", To: "2.0.0"}, nil)
	srv.autoApply(t.Context(), fn)
	if *calls != 1 {
		t.Fatalf("applyFn should be called exactly once, got %d", *calls)
	}
	if got, _ := st.LastAutoUpdate(t.Context()); got != nil {
		t.Errorf("a major bump under a patch-only policy must never be recorded as applied, got %+v", got)
	}
}

func TestAutoApply_SuccessRecordsUpdateAuditsAndRestarts(t *testing.T) {
	srv, st := newAutoApplyTestServer(t)
	if err := st.SetSelfUpdatePolicy(t.Context(), store.SelfUpdatePolicy{Enabled: true, Granularity: "patch"}); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	srv.OnRestart(func() { restarted <- struct{}{} })

	fn, calls := fakeApplyFn("1.0.1", selfupdate.Result{From: "1.0.0", To: "1.0.1"}, nil)
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
	if err := st.SetSelfUpdatePolicy(t.Context(), store.SelfUpdatePolicy{Enabled: true, Granularity: "patch"}); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	srv.OnRestart(func() { restarted <- struct{}{} })

	fn, calls := fakeApplyFn("1.0.1", selfupdate.Result{}, errors.New("checksum mismatch"))
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
