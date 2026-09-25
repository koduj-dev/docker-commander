package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
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
