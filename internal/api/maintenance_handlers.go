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
// actual schedule to recur on. Also rejects scope values that could never
// match anything (an unknown severity, an out-of-range weekday) — those would
// otherwise persist as a silently ineffective window instead of an error.
func validateMaintenanceWindow(b maintenanceWindowBody) error {
	if b.Name == "" {
		return errors.New("name is required")
	}
	if b.Reason == "" {
		return errors.New("reason is required")
	}
	for _, sev := range b.Severities {
		if !validSeverities[sev] {
			return fmt.Errorf("unknown severity %q (must be info, warning or critical)", sev)
		}
	}
	if b.Recurring {
		if len(b.Weekdays) == 0 {
			return errors.New("recurring windows need at least one weekday")
		}
		for _, d := range b.Weekdays {
			if d < time.Sunday || d > time.Saturday {
				return fmt.Errorf("weekday %d is out of range (0=Sunday..6=Saturday)", d)
			}
		}
		if _, err := time.Parse("15:04", b.TimeOfDay); err != nil {
			return errors.New("timeOfDay must be \"HH:MM\"")
		}
		if b.DurationMin <= 0 {
			return errors.New("durationMin must be positive")
		}
		if b.DurationMin > store.MaxRecurringDurationMin {
			return fmt.Errorf("a recurring occurrence can last at most %d minutes (24h)", store.MaxRecurringDurationMin)
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
	g, ok := s.resolveMaintenanceWindowGrant(r)
	return s.hostsAllowedByGrant(r.Context(), g, ok, hostIDs)
}

// maintenanceWindowGrant is the caller's effective "alerts" host-scope,
// resolved once and reused — see resolveMaintenanceWindowGrant.
type maintenanceWindowGrant struct {
	admin bool
	grant store.Grant
}

// resolveMaintenanceWindowGrant loads the caller's user record and effective
// "alerts" grant ONCE. Call this before a loop over many windows (as
// handleListMaintenanceWindows does) and reuse the result via
// hostsAllowedByGrant — calling maintenanceWindowHostsAllowed itself inside
// such a loop used to redo this (a user lookup plus several
// EffectiveGrants queries) once PER WINDOW, turning listing N windows into
// O(N) authorization queries instead of one.
func (s *Server) resolveMaintenanceWindowGrant(r *http.Request) (maintenanceWindowGrant, bool) {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		return maintenanceWindowGrant{}, false
	}
	u, err := s.store.UserByID(r.Context(), claims.UserID)
	if err != nil {
		return maintenanceWindowGrant{}, false
	}
	if u.IsAdmin() {
		return maintenanceWindowGrant{admin: true}, true
	}
	grants, err := s.store.EffectiveGrants(r.Context(), u)
	if err != nil {
		return maintenanceWindowGrant{}, false
	}
	return maintenanceWindowGrant{grant: grants["alerts"]}, true
}

// hostsAllowedByGrant applies an already-resolved grant to one window's
// scope. Empty hostIDs means "every host", which needs unrestricted host
// access — a host-restricted caller holding "alerts" must not be able to
// silence (or see) a host outside their own grant, or "alerts" would become
// a way to go deaf about a host they otherwise cannot reach.
func (s *Server) hostsAllowedByGrant(ctx context.Context, g maintenanceWindowGrant, resolved bool, hostIDs []int64) bool {
	if !resolved {
		return false
	}
	if g.admin {
		return true
	}
	if len(hostIDs) == 0 {
		return g.grant.AllHosts
	}
	for _, id := range hostIDs {
		// Grant.HasHost treats 0 as the local-daemon alias; a caller (or a
		// stored window) may instead carry the local host's REAL seeded-row
		// id, so without normalizing first a host-restricted grant would
		// wrongly refuse its own local daemon.
		if !g.grant.HasHost(s.store.NormalizeHostID(ctx, id)) {
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
		HostIDs: s.normalizeHostIDs(ctx, []int64{p.HostID}), Project: p.Slug,
		StartsAt: now, EndsAt: now.Add(s.cfg.DeploySilenceGrace),
	})
	if err != nil {
		log.Printf("project deploy: auto-silence for %q: %v", p.Slug, err)
	}
}

// maintenanceWindowView is the wire shape for a window, distinct from
// store.MaintenanceWindow so an unset (zero-value) EndsAt — a recurring
// series with no end date — serializes as an OMITTED field rather than Go's
// zero-time sentinel "0001-01-01T00:00:00Z", which the UI would otherwise
// render as a real (bogus) series end.
type maintenanceWindowView struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Reason      string         `json:"reason"`
	AuthorID    int64          `json:"authorId"`
	Author      string         `json:"author,omitempty"`
	HostIDs     []int64        `json:"hostIds"`
	Project     string         `json:"project"`
	Container   string         `json:"container"`
	RuleID      *int64         `json:"ruleId"`
	Severities  []string       `json:"severities"`
	Recurring   bool           `json:"recurring"`
	StartsAt    time.Time      `json:"startsAt"`
	EndsAt      string         `json:"endsAt,omitempty"`
	Weekdays    []time.Weekday `json:"weekdays,omitempty"`
	TimeOfDay   string         `json:"timeOfDay,omitempty"`
	DurationMin int            `json:"durationMin,omitempty"`
	Timezone    string         `json:"timezone,omitempty"`
	Ended       bool           `json:"ended"`
	CreatedAt   time.Time      `json:"createdAt"`
}

func toMaintenanceWindowView(w store.MaintenanceWindow) maintenanceWindowView {
	// HostIDs/Severities come back from the store as nil (not empty) when
	// unset — unmarshalIDs/unmarshalSections return nil for "" — and a nil
	// Go slice marshals to JSON null, not []. The frontend type promises an
	// array and calls .length on both unconditionally (an empty scope is the
	// COMMON case: every auto-silence-after-deploy window has no severity
	// restriction), so a nil here isn't cosmetic — it throws a TypeError and
	// takes down the whole Maintenance tab the moment such a row is listed.
	hostIDs := w.HostIDs
	if hostIDs == nil {
		hostIDs = []int64{}
	}
	severities := w.Severities
	if severities == nil {
		severities = []string{}
	}
	v := maintenanceWindowView{
		ID: w.ID, Name: w.Name, Reason: w.Reason, AuthorID: w.AuthorID, Author: w.Author,
		HostIDs: hostIDs, Project: w.Project, Container: w.Container, RuleID: w.RuleID, Severities: severities,
		Recurring: w.Recurring, StartsAt: w.StartsAt, Weekdays: w.Weekdays, TimeOfDay: w.TimeOfDay,
		DurationMin: w.DurationMin, Timezone: w.Timezone, Ended: w.Ended, CreatedAt: w.CreatedAt,
	}
	if !w.EndsAt.IsZero() {
		v.EndsAt = w.EndsAt.Format(time.RFC3339)
	}
	return v
}

func (s *Server) handleListMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.ListMaintenanceWindows(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list maintenance windows")
		return
	}
	// Resolved ONCE, not once per window — see resolveMaintenanceWindowGrant.
	g, ok := s.resolveMaintenanceWindowGrant(r)
	// Filtered the same way scoping is enforced on write: a window covering a
	// host outside the caller's grant would otherwise leak that host's name
	// and the window's reason to someone who cannot reach it any other way.
	windows := make([]maintenanceWindowView, 0, len(all))
	for _, win := range all {
		if s.hostsAllowedByGrant(r.Context(), g, ok, win.HostIDs) {
			windows = append(windows, toMaintenanceWindowView(win))
		}
	}
	writeJSON(w, http.StatusOK, windows)
}

// normalizeHostIDs canonicalizes every id in place — in particular, the local
// daemon's REAL seeded-row id (whatever autoincrement gave it) becomes the 0
// alias the rest of the app uses, so what's stored always matches what
// FindActiveMaintenanceWindow normalizes an alert event's host id to.
func (s *Server) normalizeHostIDs(ctx context.Context, ids []int64) []int64 {
	for i, id := range ids {
		ids[i] = s.store.NormalizeHostID(ctx, id)
	}
	return ids
}

func (s *Server) handleCreateMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var b maintenanceWindowBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	b.HostIDs = s.normalizeHostIDs(r.Context(), b.HostIDs)
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
	b.HostIDs = s.normalizeHostIDs(r.Context(), b.HostIDs)
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
