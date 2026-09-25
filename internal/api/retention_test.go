package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// newRetentionServer is newProjectServer on a FILE database, so a test can open
// a second connection and back-date rows — the store deliberately has no API for
// writing a creation time in the past.
func newRetentionServer(t *testing.T) (*Server, int64, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/dc.db"
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.CreateUser(context.Background(), &store.User{Username: "admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateProject(context.Background(), &store.Project{Name: "demo", Slug: "demo", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cph, _ := crypto.New(key)
	st.SetCipher(cph)
	srv := &Server{cfg: config.Config{DataDir: dir}, store: st, docker: docker.NewManager(st)}
	if err := os.MkdirAll(srv.projectRoot(id), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	return srv, id, raw
}

func retentionPut(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PUT", "/api/settings/retention", strings.NewReader(body)).WithContext(ctxAs(1, "admin"))
	w := httptest.NewRecorder()
	srv.handleSetRetention(w, r)
	return w
}

func TestRetentionSectionIsAdminOnly(t *testing.T) {
	for _, path := range []string{"/api/settings/retention", "/api/settings/retention/purge"} {
		if got := sectionForPath(path); got != "__admin" {
			t.Errorf("sectionForPath(%q) = %q, want __admin — retention deletes audit history", path, got)
		}
	}
}

func TestSetRetentionRejectsWhatValidateRejects(t *testing.T) {
	srv, _ := newProjectServer(t)
	for name, body := range map[string]string{
		"audit under 30 days":    `{"alertEventsDays":90,"alertDeliveriesDays":90,"auditDays":7,"revisionsKeep":50}`,
		"deliveries > events":    `{"alertEventsDays":30,"alertDeliveriesDays":60,"auditDays":365,"revisionsKeep":50}`,
		"too few revisions kept": `{"alertEventsDays":90,"alertDeliveriesDays":90,"auditDays":365,"revisionsKeep":1}`,
		"not json":               `nope`,
	} {
		if w := retentionPut(t, srv, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", name, w.Code)
		}
	}
	if got, _ := srv.store.Retention(t.Context()); got != store.DefaultRetention() {
		t.Errorf("rejected saves must leave the defaults, got %+v", got)
	}
	if w := retentionPut(t, srv, `{"alertEventsDays":30,"alertDeliveriesDays":30,"auditDays":30,"revisionsKeep":5}`); w.Code != http.StatusOK {
		t.Fatalf("valid policy → %d (%s)", w.Code, w.Body)
	}
	if got, _ := srv.store.Retention(t.Context()); got.AuditDays != 30 || got.RevisionsKeep != 5 {
		t.Errorf("policy not stored: %+v", got)
	}
}

func TestGetRetentionReportsPolicyDefaultsStatsAndLastRun(t *testing.T) {
	srv, _ := newProjectServer(t)
	r := httptest.NewRequest("GET", "/api/settings/retention", nil).WithContext(ctxAs(1, "admin"))
	w := httptest.NewRecorder()
	srv.handleGetRetention(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out struct {
		Policy, Defaults store.RetentionPolicy
		Limits           map[string]int
		Stats            store.RetentionStats
		LastRun          *store.RetentionRun
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Policy != store.DefaultRetention() || out.Defaults != store.DefaultRetention() {
		t.Errorf("defaults are ON for a fresh install: %+v / %+v", out.Policy, out.Defaults)
	}
	if out.Limits["minAuditDays"] != store.MinAuditRetentionDays || out.Stats.DBBytes <= 0 || out.LastRun != nil {
		t.Errorf("limits=%v stats=%+v lastRun=%v", out.Limits, out.Stats, out.LastRun)
	}
}

// The scheduled job and "Purge now" share runRetention: it must apply the saved
// policy, delete the snapshot FILES of trimmed revisions (not just their rows),
// and leave a record of what it did.
func TestRunRetentionAppliesThePolicyAndRecordsTheRun(t *testing.T) {
	srv, projectID, raw := newRetentionServer(t)
	ctx := t.Context()
	old := time.Now().Add(-400 * 24 * time.Hour).UTC().Format(time.RFC3339)

	if err := srv.store.SetRetention(ctx, store.RetentionPolicy{AlertEventsDays: 90, AlertDeliveriesDays: 90, AuditDays: 365, RevisionsKeep: 3}); err != nil {
		t.Fatal(err)
	}
	p, _ := srv.store.ProjectByID(ctx, projectID)
	for i := 0; i < 5; i++ {
		srv.captureRevision(ctx, p, nil, "out", "test", "tester")
	}
	revs, _ := srv.store.ListRevisions(ctx, projectID)
	if len(revs) != 5 {
		t.Fatalf("setup: %d revisions", len(revs))
	}
	for _, rv := range revs {
		if _, err := os.Stat(srv.revisionZipPath(projectID, rv.Revision)); err != nil {
			t.Fatalf("setup: snapshot for revision %d missing: %v", rv.Revision, err)
		}
	}
	if _, err := srv.store.InsertAlertEvent(ctx, &store.AlertEvent{RuleName: "r", Severity: "warning", Kind: store.KindFiring}); err != nil {
		t.Fatal(err)
	}
	// Age one event and one audit entry beyond their TTLs.
	if err := srv.store.Audit(ctx, store.AuditEntry{Action: "old.action"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE alert_events SET created_at = ?`, `UPDATE audit_log SET created_at = ?`} {
		if _, err := raw.Exec(q, old); err != nil {
			t.Fatal(err)
		}
	}

	run, err := srv.runRetention(ctx, "manual", nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.AlertEvents != 1 || run.Audit != 1 || run.Revisions != 2 || run.RevisionFiles != 2 {
		t.Errorf("run = %+v, want 1 event, 1 audit entry, 2 revisions (2 files)", run)
	}
	revs, _ = srv.store.ListRevisions(ctx, projectID)
	if len(revs) != 3 {
		t.Fatalf("%d revisions left, want the newest 3", len(revs))
	}
	for n := 1; n <= 2; n++ {
		if _, err := os.Stat(srv.revisionZipPath(projectID, n)); !os.IsNotExist(err) {
			t.Errorf("snapshot of trimmed revision %d should be gone (stat err = %v)", n, err)
		}
	}
	for _, rv := range revs {
		if _, err := os.Stat(srv.revisionZipPath(projectID, rv.Revision)); err != nil {
			t.Errorf("snapshot of kept revision %d must survive: %v", rv.Revision, err)
		}
	}
	if last, _ := srv.store.LastRetentionRun(ctx); last == nil || last.Trigger != "manual" || last.Revisions != 2 {
		t.Errorf("last run not recorded: %+v", last)
	}
	// Something was deleted, so the purge itself is in the audit log.
	entries, _ := srv.store.RecentAudit(ctx, 20, 0)
	var seen bool
	for _, e := range entries {
		seen = seen || e.Action == "retention.purge"
	}
	if !seen {
		t.Error("a purge that deleted rows must leave a retention.purge audit entry")
	}
}

func TestRunRetentionWithNothingToDoLeavesNoAuditNoise(t *testing.T) {
	srv, _ := newProjectServer(t)
	run, err := srv.runRetention(t.Context(), "scheduled", nil)
	if err != nil || run.Total() != 0 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	entries, _ := srv.store.RecentAudit(t.Context(), 20, 0)
	for _, e := range entries {
		if e.Action == "retention.purge" {
			t.Error("a daily purge that deleted nothing must not write an audit entry")
		}
	}
}

func TestRunRetentionRefusesToRunTwiceAtOnce(t *testing.T) {
	srv, _ := newProjectServer(t)
	srv.retentionMu.Lock()
	defer srv.retentionMu.Unlock()
	if _, err := srv.runRetention(t.Context(), "manual", nil); err != errRetentionBusy {
		t.Errorf("err = %v, want errRetentionBusy", err)
	}
	w := httptest.NewRecorder()
	srv.handlePurgeRetention(w, httptest.NewRequest("POST", "/api/settings/retention/purge", nil).WithContext(ctxAs(1, "admin")))
	if w.Code != http.StatusConflict {
		t.Errorf("purge while one runs → %d, want 409", w.Code)
	}
}

// A settings row that cannot be read must stop the purge cold. Falling back to
// the defaults would delete data an admin chose to keep forever.
func TestRunRetentionAbortsOnAnUnreadablePolicyAndDeletesNothing(t *testing.T) {
	srv, _, raw := newRetentionServer(t)
	ctx := t.Context()
	if err := srv.store.SetRetention(ctx, store.RetentionPolicy{}); err != nil { // keep forever
		t.Fatal(err)
	}
	if _, err := srv.store.InsertAlertEvent(ctx, &store.AlertEvent{RuleName: "r", Severity: "warning", Kind: store.KindFiring}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-1000 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := raw.Exec(`UPDATE alert_events SET created_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE settings SET value = '{corrupt' WHERE key = 'retention.policy'`); err != nil {
		t.Fatal(err)
	}

	run, err := srv.runRetention(ctx, "scheduled", nil)
	if !errors.Is(err, store.ErrRetentionPolicyInvalid) || run != nil {
		t.Fatalf("run=%+v err=%v, want a refusal", run, err)
	}
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM alert_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d alert events left, want 1 — nothing may be deleted", n)
	}
	if last, _ := srv.store.LastRetentionRun(ctx); last != nil {
		t.Errorf("a refused run must not be recorded as a purge: %+v", last)
	}

	// The page still loads (showing the defaults plus the problem) and a manual
	// purge explains itself instead of failing opaquely.
	w := httptest.NewRecorder()
	srv.handleGetRetention(w, httptest.NewRequest("GET", "/api/settings/retention", nil).WithContext(ctxAs(1, "admin")))
	var out struct{ PolicyError string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || out.PolicyError == "" {
		t.Errorf("GET → %d, policyError=%q, want 200 with the problem reported", w.Code, out.PolicyError)
	}
	w = httptest.NewRecorder()
	srv.handlePurgeRetention(w, httptest.NewRequest("POST", "/api/settings/retention/purge", nil).WithContext(ctxAs(1, "admin")))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Nothing was deleted") {
		t.Errorf("purge → %d %s, want 422 saying nothing was deleted", w.Code, w.Body)
	}
	// Saving a valid policy is the way out.
	if w := retentionPut(t, srv, `{"alertEventsDays":90,"alertDeliveriesDays":90,"auditDays":365,"revisionsKeep":50}`); w.Code != http.StatusOK {
		t.Fatalf("save → %d", w.Code)
	}
	if _, err := srv.runRetention(ctx, "manual", nil); err != nil {
		t.Errorf("after saving a valid policy the purge runs again: %v", err)
	}
}

// Snapshot files with no revision row (a purge that stopped part-way, or a file
// that could not be removed) are found and removed by a later run.
func TestRunRetentionSweepsOrphanedSnapshotFiles(t *testing.T) {
	srv, projectID, _ := newRetentionServer(t)
	ctx := t.Context()
	p, _ := srv.store.ProjectByID(ctx, projectID)
	srv.captureRevision(ctx, p, nil, "out", "test", "tester") // revision 1: row + file
	dir := srv.projectRevisionsDir(projectID)

	orphan := srv.revisionZipPath(projectID, 77) // no row
	tmp := dir + "/.revision-123.zip.tmp"        // in-flight capture
	note := dir + "/README.txt"                  // not ours
	strayDir := srv.projectRevisionsDir(999) + "/5.zip"
	for _, f := range []string{orphan, tmp, note} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(srv.projectRevisionsDir(999), 0o700); err != nil { // a project that has no rows at all
		t.Fatal(err)
	}
	if err := os.WriteFile(strayDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	run, err := srv.runRetention(ctx, "scheduled", nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.RevisionFiles != 2 {
		t.Errorf("RevisionFiles = %d, want 2 (the orphan and the stray project's file)", run.RevisionFiles)
	}
	for _, gone := range []string{orphan, strayDir} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should have been swept (stat err = %v)", gone, err)
		}
	}
	for _, kept := range []string{srv.revisionZipPath(projectID, 1), tmp, note} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s must be left alone: %v", kept, err)
		}
	}
}

// A file that cannot be removed is reported in the run, not just logged, and is
// retried by the next run instead of being forgotten.
func TestRunRetentionReportsASnapshotItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	srv, projectID, _ := newRetentionServer(t)
	ctx := t.Context()
	orphan := srv.revisionZipPath(projectID, 5)
	if err := os.MkdirAll(srv.projectRevisionsDir(projectID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := srv.projectRevisionsDir(projectID)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	run, err := srv.runRetention(ctx, "manual", nil)
	if err == nil || run == nil || !strings.Contains(run.Error, "remove orphan snapshot") {
		t.Fatalf("run=%+v err=%v, want the failed removal reported", run, err)
	}
	if last, _ := srv.store.LastRetentionRun(ctx); last == nil || last.Error == "" {
		t.Errorf("the failure must be stored with the run: %+v", last)
	}

	_ = os.Chmod(dir, 0o700)
	if run, err := srv.runRetention(ctx, "manual", nil); err != nil || run.RevisionFiles != 1 {
		t.Errorf("the next run must retry and succeed: run=%+v err=%v", run, err)
	}
}
