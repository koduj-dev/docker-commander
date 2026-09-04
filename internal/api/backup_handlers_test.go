package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/volume"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// newBackupJobsServer builds a Server with a cipher (needed for env
// encryption) and an admin user — everything the backup-job handlers touch.
func newBackupJobsServer(t *testing.T) (*Server, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cph, _ := crypto.New(key)
	st.SetCipher(cph)
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: config.Config{DataDir: t.TempDir()}, store: st, docker: docker.NewManager(st)}
	admin, err := st.CreateUser(context.Background(), &store.User{Username: "admin", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return srv, admin
}

func ctxAsNamed(uid int64, username, role string) context.Context {
	return auth.WithClaims(context.Background(), &auth.Claims{UserID: uid, Username: username, Role: role})
}

func TestBackupJobs_CreateListGetUpdateDelete(t *testing.T) {
	srv, admin := newBackupJobsServer(t)

	createBody := `{"name":"nightly","enabled":true,"scope":"volume","volumeName":"data","hostId":1,
		"image":"restic/restic","command":"restic backup /data","intervalMinutes":60,
		"env":{"RESTIC_PASSWORD":"s3cret"}}`
	r := httptest.NewRequest("POST", "/api/backup-jobs", strings.NewReader(createBody)).WithContext(ctxAsNamed(admin, "admin", "admin"))
	w := httptest.NewRecorder()
	srv.handleCreateBackupJob(w, r)
	if w.Code != 200 {
		t.Fatalf("create status = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// List must not leak env.
	r = httptest.NewRequest("GET", "/api/backup-jobs", nil).WithContext(ctxAsNamed(admin, "admin", "admin"))
	w = httptest.NewRecorder()
	srv.handleListBackupJobs(w, r)
	if w.Code != 200 {
		t.Fatalf("list status = %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("s3cret")) {
		t.Fatal("SECURITY: list response echoed the plaintext env secret")
	}
	var list []store.BackupJob
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "nightly" || list[0].CreatedBy != "admin" {
		t.Errorf("list = %+v", list)
	}

	// Get single, same guarantee.
	idStr := strconv.FormatInt(created.ID, 10)
	r = httptest.NewRequest("GET", "/api/backup-jobs/"+idStr, nil).WithContext(ctxAsNamed(admin, "admin", "admin"))
	r = withURLParam(r, "id", idStr)
	w = httptest.NewRecorder()
	srv.handleGetBackupJob(w, r)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("s3cret")) {
		t.Fatalf("get status=%d body=%s", w.Code, w.Body.String())
	}

	// Update.
	updateBody := `{"name":"nightly2","scope":"volume","volumeName":"data2","hostId":1,
		"image":"restic/restic","command":"restic backup /data","intervalMinutes":30,
		"env":{"RESTIC_PASSWORD":"newpw"}}`
	r = httptest.NewRequest("PUT", "/api/backup-jobs/"+idStr, strings.NewReader(updateBody)).WithContext(ctxAsNamed(admin, "admin", "admin"))
	r = withURLParam(r, "id", idStr)
	w = httptest.NewRecorder()
	srv.handleUpdateBackupJob(w, r)
	if w.Code != 200 {
		t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
	}
	job, err := srv.store.BackupJobByID(context.Background(), created.ID)
	if err != nil || job.Name != "nightly2" || job.VolumeName != "data2" {
		t.Errorf("update not applied: %+v err=%v", job, err)
	}
	env, err := srv.store.BackupJobEnv(context.Background(), created.ID)
	if err != nil || env["RESTIC_PASSWORD"] != "newpw" {
		t.Errorf("env not updated: %+v err=%v", env, err)
	}

	// Toggle enabled.
	r = httptest.NewRequest("PATCH", "/api/backup-jobs/"+idStr, strings.NewReader(`{"enabled":false}`)).WithContext(ctxAsNamed(admin, "admin", "admin"))
	r = withURLParam(r, "id", idStr)
	w = httptest.NewRecorder()
	srv.handleSetBackupJobEnabled(w, r)
	if w.Code != 200 {
		t.Fatalf("toggle status = %d", w.Code)
	}
	job, _ = srv.store.BackupJobByID(context.Background(), created.ID)
	if job.Enabled {
		t.Error("expected job disabled")
	}

	// Delete.
	r = httptest.NewRequest("DELETE", "/api/backup-jobs/"+idStr, nil).WithContext(ctxAsNamed(admin, "admin", "admin"))
	r = withURLParam(r, "id", idStr)
	w = httptest.NewRecorder()
	srv.handleDeleteBackupJob(w, r)
	if w.Code != 200 {
		t.Fatalf("delete status = %d", w.Code)
	}
	if _, err := srv.store.BackupJobByID(context.Background(), created.ID); err != store.ErrNotFound {
		t.Errorf("expected job gone after delete, got %v", err)
	}

	// Audit trail recorded the config changes.
	entries, _ := srv.store.RecentAudit(context.Background(), 10, 0)
	actions := map[string]bool{}
	for _, e := range entries {
		actions[e.Action] = true
	}
	for _, want := range []string{"backup_job.create", "backup_job.update", "backup_job.delete"} {
		if !actions[want] {
			t.Errorf("expected an audit entry for %q, got %+v", want, entries)
		}
	}
}

func TestBackupJobs_CreateValidation(t *testing.T) {
	srv, admin := newBackupJobsServer(t)
	for _, body := range []string{
		`{}`,
		`{"name":"x","image":"i","command":"c","scope":"volume"}`,      // missing volumeName
		`{"name":"x","image":"i","command":"c","scope":"project"}`,     // missing projectId
		`{"name":"x","image":"i","command":"c","scope":"bogus"}`,       // invalid scope
		`{"name":"x","scope":"volume","volumeName":"v","command":"c"}`, // missing image
	} {
		r := httptest.NewRequest("POST", "/api/backup-jobs", strings.NewReader(body)).WithContext(ctxAsNamed(admin, "admin", "admin"))
		w := httptest.NewRecorder()
		srv.handleCreateBackupJob(w, r)
		if w.Code != 400 {
			t.Errorf("body %q: status = %d, want 400", body, w.Code)
		}
	}
	list, _ := srv.store.ListBackupJobs(context.Background())
	if len(list) != 0 {
		t.Errorf("no invalid job should have been created, got %d", len(list))
	}
}

func TestBackupJobs_RunNowAndListRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a docker daemon; skipped under -short")
	}
	srv, admin := newBackupJobsServer(t)
	if _, err := srv.docker.Client(context.Background(), 0); err != nil {
		t.Skip("docker not reachable")
	}
	if _, err := srv.docker.SystemInfo(context.Background(), 0); err != nil {
		t.Skipf("docker daemon not available: %v", err)
	}

	id, err := srv.store.CreateBackupJob(context.Background(), &store.BackupJob{
		Name: "job", Scope: store.BackupScopeVolume, VolumeName: "dc-backupjobs-apitest-vol",
		Image: "alpine:latest", Command: "true",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := srv.docker.Client(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.VolumeCreate(context.Background(), volume.CreateOptions{Name: "dc-backupjobs-apitest-vol"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), "dc-backupjobs-apitest-vol", true) })

	idStr := strconv.FormatInt(id, 10)
	r := httptest.NewRequest("POST", "/api/backup-jobs/"+idStr+"/run", nil).WithContext(ctxAsNamed(admin, "alice", "admin"))
	r = withURLParam(r, "id", idStr)
	w := httptest.NewRecorder()
	srv.handleRunBackupJob(w, r)
	if w.Code != 200 {
		t.Fatalf("run status = %d: %s", w.Code, w.Body.String())
	}

	r = httptest.NewRequest("GET", "/api/backup-jobs/"+idStr+"/runs", nil).WithContext(ctxAsNamed(admin, "admin", "admin"))
	r = withURLParam(r, "id", idStr)
	w = httptest.NewRecorder()
	srv.handleListBackupRuns(w, r)
	var runs []store.BackupRun
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].TriggeredBy != "alice" || !runs[0].OK {
		t.Errorf("runs = %+v", runs)
	}

	entries, _ := srv.store.RecentAudit(context.Background(), 10, 0)
	found := false
	for _, e := range entries {
		if e.Action == "backup_job.run" {
			found = true
		}
	}
	if !found {
		t.Error("expected a backup_job.run audit entry for the manual trigger")
	}
}
