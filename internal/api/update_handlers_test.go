package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUpdateCheckerDisabled(t *testing.T) {
	u := newUpdateChecker("1.2.0", false, true)
	st := u.status(t.Context())
	if !st.Disabled {
		t.Error("disabled checker should report Disabled")
	}
	if st.UpdateAvailable {
		t.Error("disabled checker must never report an available update")
	}
	if st.Current != "1.2.0" {
		t.Errorf("Current = %q, want 1.2.0", st.Current)
	}
}

// apply must refuse when either the update check or self-update is turned off —
// without making any outbound call.
func TestApplyRefusedWhenDisabled(t *testing.T) {
	cases := []struct {
		name             string
		enabled, selfUpd bool
	}{
		{"update-check off", false, true},
		{"self-update off", true, false},
		{"both off", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := newUpdateChecker("1.0.0", c.enabled, c.selfUpd)
			if _, err := u.apply(t.Context()); !errors.Is(err, errSelfUpdateDisabled) {
				t.Errorf("apply err = %v, want errSelfUpdateDisabled", err)
			}
		})
	}
}

// PENTEST: a non-admin must never reach apply/restart. The router maps the
// "update" section to __admin; here we assert apply is gated behind the
// self-update flag even if a request reaches the handler, and that restart is
// not offered (501) when the process can't re-exec.
func TestRestartUnsupportedReturns501(t *testing.T) {
	srv := &Server{update: newUpdateChecker("1.0.0", true, true)} // onRestart nil
	w := httptest.NewRecorder()
	srv.handleRestart(w, httptest.NewRequest("POST", "/api/update/restart", nil))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("restart without a hook should be 501, got %d", w.Code)
	}
}

func TestRestartRefusedWhenSelfUpdateOff(t *testing.T) {
	called := false
	srv := &Server{update: newUpdateChecker("1.0.0", true, false)}
	srv.OnRestart(func() { called = true })
	w := httptest.NewRecorder()
	srv.handleRestart(w, httptest.NewRequest("POST", "/api/update/restart", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("restart with self-update off should be 403, got %d", w.Code)
	}
	if called {
		t.Error("onRestart must not fire when self-update is disabled")
	}
}

// handleApplyUpdate surfaces the disabled state as 403 without doing any work.
func TestHandleApplyDisabled(t *testing.T) {
	srv := &Server{update: newUpdateChecker("1.0.0", true, false)}
	w := httptest.NewRecorder()
	srv.handleApplyUpdate(w, httptest.NewRequest("POST", "/api/update", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("apply with self-update off should be 403, got %d", w.Code)
	}
}

// The status handler advertises the one-tap button only when self-update is
// allowed AND a restart hook is wired. The cache is pre-seeded so status()
// returns without an outbound GitHub call.
func TestStatusSelfUpdateFlag(t *testing.T) {
	seed := func(enabled, selfUpd, hook bool) updateStatus {
		u := newUpdateChecker("1.0.0", enabled, selfUpd)
		u.ok, u.at, u.cached = true, time.Now(), updateStatus{Current: "1.0.0"}
		srv := &Server{update: u}
		if hook {
			srv.OnRestart(func() {})
		}
		w := httptest.NewRecorder()
		srv.handleUpdateStatus(w, httptest.NewRequest("GET", "/api/update", nil))
		var st updateStatus
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return st
	}
	if !seed(true, true, true).SelfUpdate {
		t.Error("expected SelfUpdate=true when enabled+selfUpdate+hook all present")
	}
	if seed(true, true, false).SelfUpdate {
		t.Error("SelfUpdate must be false without a restart hook")
	}
	if seed(true, false, true).SelfUpdate {
		t.Error("SelfUpdate must be false when self-update is disabled")
	}
}
