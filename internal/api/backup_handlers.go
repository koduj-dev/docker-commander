package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/backupjobs"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// backupJobBody mirrors store.BackupJob for create/update requests, plus the
// write-only env map (never echoed back — see store.BackupJob's own doc).
type backupJobBody struct {
	Name            string            `json:"name"`
	Enabled         bool              `json:"enabled"`
	Scope           string            `json:"scope"`
	VolumeName      string            `json:"volumeName"`
	ProjectID       int64             `json:"projectId"`
	HostID          int64             `json:"hostId"`
	Image           string            `json:"image"`
	Command         string            `json:"command"`
	IntervalMinutes int               `json:"intervalMinutes"`
	Env             map[string]string `json:"env"`
}

// validateBackupJobBody checks the fields RunJob's scope switch depends on are
// actually present, so a malformed job fails at creation rather than at its
// first (or every) run.
func validateBackupJobBody(b backupJobBody) string {
	if b.Name == "" {
		return "name is required"
	}
	if b.Image == "" || b.Command == "" {
		return "image and command are required"
	}
	switch b.Scope {
	case store.BackupScopeVolume:
		if b.VolumeName == "" {
			return "volumeName is required for a volume-scoped job"
		}
	case store.BackupScopeProject:
		if b.ProjectID == 0 {
			return "projectId is required for a project-scoped job"
		}
	default:
		return "scope must be \"volume\" or \"project\""
	}
	if b.IntervalMinutes < 0 {
		return "intervalMinutes cannot be negative"
	}
	return ""
}

func (s *Server) handleListBackupJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListBackupJobs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list backup jobs")
		return
	}
	if jobs == nil {
		jobs = []store.BackupJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleGetBackupJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	job, err := s.store.BackupJobByID(r.Context(), id)
	if err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "backup job not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load backup job")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCreateBackupJob(w http.ResponseWriter, r *http.Request) {
	var b backupJobBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if msg := validateBackupJobBody(b); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	createdBy := ""
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		createdBy = c.Username
	}
	job := &store.BackupJob{
		Name: b.Name, Enabled: b.Enabled, Scope: b.Scope, VolumeName: b.VolumeName,
		ProjectID: b.ProjectID, HostID: b.HostID, Image: b.Image, Command: b.Command,
		IntervalMinutes: b.IntervalMinutes, CreatedBy: createdBy,
	}
	id, err := s.store.CreateBackupJob(r.Context(), job, b.Env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create backup job")
		return
	}
	s.audit(r, "backup_job.create", b.Name, b.Scope)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleUpdateBackupJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var b backupJobBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if msg := validateBackupJobBody(b); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	job := &store.BackupJob{
		Name: b.Name, Scope: b.Scope, VolumeName: b.VolumeName, ProjectID: b.ProjectID,
		HostID: b.HostID, Image: b.Image, Command: b.Command, IntervalMinutes: b.IntervalMinutes,
	}
	if err := s.store.UpdateBackupJob(r.Context(), id, job, b.Env); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update backup job")
		return
	}
	s.audit(r, "backup_job.update", b.Name, b.Scope)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSetBackupJobEnabled(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetBackupJobEnabled(r.Context(), id, body.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update backup job")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteBackupJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.DeleteBackupJob(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete backup job")
		return
	}
	s.audit(r, "backup_job.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRunBackupJob triggers a job immediately, bypassing its schedule. This
// is the only backup-job action that's audited as an attributable-to-a-user
// action — a scheduled run is recorded in backup_runs/status instead,
// mirroring alert firing vs. alert-rule CRUD.
func (s *Server) handleRunBackupJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	triggeredBy := "admin"
	if c, ok := auth.ClaimsFrom(r.Context()); ok && c.Username != "" {
		triggeredBy = c.Username
	}
	if err := backupjobs.TriggerNow(r.Context(), s.store, s.docker, id, triggeredBy); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "backup_job.run", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListBackupRuns(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	runs, err := s.store.ListBackupRuns(r.Context(), id, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list backup runs")
		return
	}
	if runs == nil {
		runs = []store.BackupRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}
