package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/selfupdate"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// The "Update & restart" button is only worth offering when the install would
// go through. A packaged binary (managed by apt/dnf) or a directory the
// service can't write gets an explanation and a manual command instead of a
// button that is bound to fail.

func statusServer(t *testing.T, precheck func() error) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u := newUpdateChecker("1.6.2", true, true)
	u.precheck = precheck
	// Pre-seed the GitHub answer so the handler never goes to the network.
	u.cached, u.at, u.ok = updateStatus{Current: "1.6.2", Latest: "1.7.2", UpdateAvailable: true}, time.Now(), true
	srv := &Server{store: st, update: u}
	srv.OnRestart(func() {})
	return srv
}

func getStatus(t *testing.T, srv *Server) updateStatus {
	t.Helper()
	w := httptest.NewRecorder()
	srv.handleUpdateStatus(w, httptest.NewRequest("GET", "/api/update/status", nil))
	var st updateStatus
	if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestUpdateStatus_OffersButtonWhenInstallCanRun(t *testing.T) {
	st := getStatus(t, statusServer(t, func() error { return nil }))
	if !st.SelfUpdate {
		t.Error("selfUpdate should be true when the precheck passes")
	}
	if st.SelfUpdateBlocked != "" || st.UpdateCommand != "" {
		t.Errorf("no reason expected, got %q / %q", st.SelfUpdateBlocked, st.UpdateCommand)
	}
}

func TestUpdateStatus_PackagedBinaryPointsAtPackageManager(t *testing.T) {
	st := getStatus(t, statusServer(t, func() error {
		return &selfupdate.ErrPackageManaged{Exe: "/usr/bin/dockercmd", Manager: "apt"}
	}))
	if st.SelfUpdate {
		t.Error("the button must not be offered for a packaged binary")
	}
	if !strings.Contains(st.SelfUpdateBlocked, "package") {
		t.Errorf("reason = %q, want it to say it's a package install", st.SelfUpdateBlocked)
	}
	if st.UpdateCommand != "sudo apt update && sudo apt install --only-upgrade dockercmd" {
		t.Errorf("command = %q", st.UpdateCommand)
	}
	if !st.UpdateAvailable {
		t.Error("the update itself must still be announced")
	}
}

func TestUpdateStatus_UnwritableDirExplainsAndOffersCLI(t *testing.T) {
	st := getStatus(t, statusServer(t, func() error {
		return &selfupdate.ErrNotWritable{Dir: "/usr/local/bin", Err: errors.New("read-only file system")}
	}))
	if st.SelfUpdate {
		t.Error("the button must not be offered when the directory isn't writable")
	}
	if !strings.Contains(st.SelfUpdateBlocked, "/usr/local/bin") {
		t.Errorf("reason = %q, want the directory named", st.SelfUpdateBlocked)
	}
	if st.UpdateCommand != "sudo dockercmd --self-upgrade" {
		t.Errorf("command = %q", st.UpdateCommand)
	}
}

func TestUpdateStatus_NoPrecheckWhenSelfUpdateIsOff(t *testing.T) {
	srv := statusServer(t, func() error { t.Error("precheck must not run when self-update is off"); return nil })
	srv.update.selfUpdate = false
	if st := getStatus(t, srv); st.SelfUpdate || st.SelfUpdateBlocked != "" {
		t.Errorf("got selfUpdate=%v blocked=%q, want neither", st.SelfUpdate, st.SelfUpdateBlocked)
	}
}

func TestUpdateStatus_PrecheckIsCached(t *testing.T) {
	calls := 0
	srv := statusServer(t, func() error { calls++; return nil })
	for range 5 {
		getStatus(t, srv)
	}
	if calls != 1 {
		t.Errorf("precheck ran %d times for 5 status loads, want 1", calls)
	}
	srv.update.preAt = time.Now().Add(-precheckTTL)
	getStatus(t, srv)
	if calls != 2 {
		t.Errorf("precheck should re-run once its answer is stale, ran %d times", calls)
	}
}
