package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// maintenanceWindowBody mirrors store.MaintenanceWindow but omits the fields
// the server decides, not the caller: ID, AuthorID/Author, Ended, CreatedAt.
type maintenanceWindowBody struct {
	Name        string         `json:"name"`
	Reason      string         `json:"reason"`
	HostIDs     []int64        `json:"hostIds"`
	Project     string         `json:"project"`
	Container   string         `json:"container"`
	RuleID      *int64         `json:"ruleId"`
	Severities  []string       `json:"severities"`
	Recurring   bool           `json:"recurring"`
	StartsAt    time.Time      `json:"startsAt"`
	EndsAt      time.Time      `json:"endsAt"`
	Weekdays    []time.Weekday `json:"weekdays"`
	TimeOfDay   string         `json:"timeOfDay"`
	DurationMin int            `json:"durationMin"`
	Timezone    string         `json:"timezone"`
}

func (b maintenanceWindowBody) toWindow() *store.MaintenanceWindow {
	return &store.MaintenanceWindow{
		Name: b.Name, Reason: b.Reason, HostIDs: b.HostIDs, Project: b.Project, Container: b.Container,
		RuleID: b.RuleID, Severities: b.Severities, Recurring: b.Recurring, StartsAt: b.StartsAt, EndsAt: b.EndsAt,
		Weekdays: b.Weekdays, TimeOfDay: b.TimeOfDay, DurationMin: b.DurationMin, Timezone: b.Timezone,
	}
}

// validateMaintenanceWindow enforces the shape NEXT.md describes: a reason is
// mandatory (this suppresses paging — "why" must always be answerable later),
// a one-off window needs a real start/end, and a recurring one needs an
// actual schedule to recur on.
func validateMaintenanceWindow(b maintenanceWindowBody) error {
	if b.Name == "" {
		return errors.New("name is required")
	}
	if b.Reason == "" {
		return errors.New("reason is required")
	}
	if b.Recurring {
		if len(b.Weekdays) == 0 {
			return errors.New("recurring windows need at least one weekday")
		}
		if _, err := time.Parse("15:04", b.TimeOfDay); err != nil {
			return errors.New("timeOfDay must be \"HH:MM\"")
		}
		if b.DurationMin <= 0 {
			return errors.New("durationMin must be positive")
		}
		if b.StartsAt.IsZero() {
			return errors.New("startsAt is required (when the series begins)")
		}
		if !b.EndsAt.IsZero() && !b.EndsAt.After(b.StartsAt) {
			return errors.New("endsAt, if set, must be after startsAt")
		}
		if b.Timezone != "" {
			if _, err := time.LoadLocation(b.Timezone); err != nil {
				return fmt.Errorf("unknown timezone %q", b.Timezone)
			}
		}
		return nil
	}
	if b.StartsAt.IsZero() || b.EndsAt.IsZero() {
		return errors.New("startsAt and endsAt are required for a one-off window")
	}
	if !b.EndsAt.After(b.StartsAt) {
		return errors.New("endsAt must be after startsAt")
	}
	return nil
}

// maintenanceWindowHostsAllowed reports whether the caller may set a window's
// scope to hostIDs. A maintenance window silences alert DELIVERY for whatever
// hosts it names — unlike an alert rule (which names none at all today), a
// host-restricted caller holding the "alerts" section must not be able to
// silence a host outside their own grant, or "alerts" would become a way to
// go deaf about a host you otherwise cannot see. Empty hostIDs means "every
// host", which needs unrestricted host access for the same reason.
func (s *Server) maintenanceWindowHostsAllowed(r *http.Request, hostIDs []int64) bool {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		return false
	}
	u, err := s.store.UserByID(r.Context(), claims.UserID)
	if err != nil {
		return false
	}
	if u.IsAdmin() {
		return true
	}
	grants, err := s.store.EffectiveGrants(r.Context(), u)
	if err != nil {
		return false
	}
	g := grants["alerts"]
	if len(hostIDs) == 0 {
		return g.AllHosts
	}
	for _, id := range hostIDs {
		if !g.HasHost(id) {
			return false
		}
	}
	return true
}

// autoSilenceForDeploy suppresses alert delivery for a project's host+stack
// for a configurable grace period right after a successful deploy —
// containers restarting, warming up, or briefly failing a health check are
// expected noise from the deploy itself, not a new incident. Best-effort,
// like the revision capture and audit calls around it: the deploy already
// succeeded, so a failure here must not turn into a user-facing error, only
// a log line.
//
// Called from both the REST and MCP deploy paths (project_handlers.go's
// handleDeployProject, mcp_projects.go's mcpDeployProject) — there is no
// shared "after a successful deploy" hook in this codebase to attach to
// instead, so both call sites call this directly, matching how they already
// each call captureRevision and their own audit action independently.
func (s *Server) autoSilenceForDeploy(ctx context.Context, p *store.Project) {
	if s.cfg.DeploySilenceGrace <= 0 {
		return
	}
	now := time.Now()
	_, err := s.store.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "auto: " + p.Name + " deploy", Reason: "automatic grace period after a deploy",
		HostIDs: []int64{p.HostID}, Project: p.Slug,
		StartsAt: now, EndsAt: now.Add(s.cfg.DeploySilenceGrace),
	})
	if err != nil {
		log.Printf("project deploy: auto-silence for %q: %v", p.Slug, err)
	}
}

func (s *Server) handleListMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.ListMaintenanceWindows(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list maintenance windows")
		return
	}
	// Filtered the same way scoping is enforced on write: a window covering a
	// host outside the caller's grant would otherwise leak that host's name
	// and the window's reason to someone who cannot reach it any other way.
	windows := make([]store.MaintenanceWindow, 0, len(all))
	for _, win := range all {
		if s.maintenanceWindowHostsAllowed(r, win.HostIDs) {
			windows = append(windows, win)
		}
	}
	writeJSON(w, http.StatusOK, windows)
}

func (s *Server) handleCreateMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var b maintenanceWindowBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := validateMaintenanceWindow(b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.maintenanceWindowHostsAllowed(r, b.HostIDs) {
		writeErr(w, http.StatusForbidden, "cannot scope a maintenance window to a host outside your access")
		return
	}
	win := b.toWindow()
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		win.AuthorID = claims.UserID
	}
	id, err := s.store.CreateMaintenanceWindow(r.Context(), win)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create maintenance window")
		return
	}
	s.audit(r, "maintenance_window.create", b.Name, b.Reason)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleUpdateMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var b maintenanceWindowBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := validateMaintenanceWindow(b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The EXISTING window must already be in reach (an out-of-scope window
	// must not even be discoverable as editable) AND the new scope must be
	// too (an in-reach window must not be reassigned to silence a host the
	// caller cannot see).
	if _, err := s.maintenanceWindowInReach(r, id); err != nil {
		writeErr(w, http.StatusNotFound, "maintenance window not found")
		return
	}
	if !s.maintenanceWindowHostsAllowed(r, b.HostIDs) {
		writeErr(w, http.StatusForbidden, "cannot scope a maintenance window to a host outside your access")
		return
	}
	if err := s.store.UpdateMaintenanceWindow(r.Context(), id, b.toWindow()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "maintenance window not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not update maintenance window")
		return
	}
	s.audit(r, "maintenance_window.update", b.Name, b.Reason)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// maintenanceWindowInReach loads a window and reports whether the caller may
// act on it — same host-scope reasoning as maintenanceWindowHostsAllowed,
// applied to a window that already exists. A missing window and one outside
// the caller's access give the SAME answer (ErrNotFound, mapped to 404 by
// both callers below): distinguishing them would let a host-scoped caller
// probe which window ids exist on hosts they cannot see.
func (s *Server) maintenanceWindowInReach(r *http.Request, id int64) (*store.MaintenanceWindow, error) {
	win, err := s.store.MaintenanceWindowByID(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !s.maintenanceWindowHostsAllowed(r, win.HostIDs) {
		return nil, store.ErrNotFound
	}
	return win, nil
}

// handleEndMaintenanceWindow ends a window early — the work finished ahead of
// schedule — without deleting its row or audit trail.
func (s *Server) handleEndMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if _, err := s.maintenanceWindowInReach(r, id); err != nil {
		writeErr(w, http.StatusNotFound, "maintenance window not found")
		return
	}
	if err := s.store.EndMaintenanceWindow(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not end maintenance window")
		return
	}
	s.audit(r, "maintenance_window.end", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if _, err := s.maintenanceWindowInReach(r, id); err != nil {
		writeErr(w, http.StatusNotFound, "maintenance window not found")
		return
	}
	if err := s.store.DeleteMaintenanceWindow(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete maintenance window")
		return
	}
	s.audit(r, "maintenance_window.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
