package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// MaintenanceWindow suppresses alert DELIVERY for the scope and time it
// covers, without stopping the engine from noticing or recording what
// happened — the point is to stop the paging, not the observing (see
// AlertEvent.Suppressed). Distinct from a disabled host, which the engine
// does not watch at all.
//
// Every scope field left at its zero value means "no restriction on this
// dimension" — the same convention AlertRule.Target and api_tokens.host_ids
// already use — so a window with no scope set at all silences everything.
type MaintenanceWindow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
	AuthorID int64  `json:"authorId"`
	Author   string `json:"author,omitempty"` // joined username, list reads only

	HostIDs    []int64  `json:"hostIds"`
	Project    string   `json:"project"`   // compose project (stack) name substring
	Container  string   `json:"container"` // container name substring
	RuleID     *int64   `json:"ruleId"`
	Severities []string `json:"severities"`

	Recurring bool `json:"recurring"`
	// One-off: the exact window. Recurring: StartsAt's date is when the series
	// begins (its time-of-day is ignored — see TimeOfDay), EndsAt is when the
	// whole series stops recurring (zero = indefinitely).
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
	// Recurring-only. Weekdays uses time.Weekday's own numbering (0=Sunday).
	Weekdays    []time.Weekday `json:"weekdays,omitempty"`
	TimeOfDay   string         `json:"timeOfDay,omitempty"` // "HH:MM", 24h, in Timezone
	DurationMin int            `json:"durationMin,omitempty"`
	Timezone    string         `json:"timezone,omitempty"` // IANA name; "" = UTC

	// Ended lets an operator end a window early (the work finished ahead of
	// schedule) without deleting it — the row, and the audit trail around it,
	// stay in place.
	Ended     bool      `json:"ended"`
	CreatedAt time.Time `json:"createdAt"`
}

// Active reports whether the window covers the instant now, independent of
// scope — a one-off window is active between its start and end; a recurring
// one is active during any occurrence its schedule produces. An ended window
// is never active, regardless of its schedule.
func (w MaintenanceWindow) Active(now time.Time) bool {
	_, _, ok := w.ActiveSpan(now)
	return ok
}

// ActiveSpan is Active plus the bounds of the occurrence covering now: the
// window's own start/end for a one-off, the current occurrence's for a
// recurring one. It is what a "window started / ended" log line reports.
func (w MaintenanceWindow) ActiveSpan(now time.Time) (start, end time.Time, ok bool) {
	if w.Ended {
		return time.Time{}, time.Time{}, false
	}
	if !w.Recurring {
		if !now.Before(w.StartsAt) && now.Before(w.EndsAt) {
			return w.StartsAt, w.EndsAt, true
		}
		return time.Time{}, time.Time{}, false
	}
	return w.recurringSpanAt(now)
}

// recurringSpanAt checks both today's occurrence and yesterday's, so a
// window whose occurrence spans midnight in its own timezone (e.g. starts
// 23:00 for 120 minutes) is still found active just after midnight, when the
// occurrence that covers "now" actually started on the previous calendar day.
// This two-day lookback is only sufficient because DurationMin is capped at
// 24h for a recurring window (enforced where one is created/updated) — any
// occurrence that could still cover "now" therefore started within the last
// 24h, which both candidate days together always contain.
func (w MaintenanceWindow) recurringSpanAt(now time.Time) (start, end time.Time, ok bool) {
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil || w.Timezone == "" {
		loc = time.UTC
	}
	hh, mm, parsed := parseTimeOfDay(w.TimeOfDay)
	if !parsed || w.DurationMin <= 0 {
		return time.Time{}, time.Time{}, false
	}
	// The series begins on StartsAt's CALENDAR DATE in the schedule's own
	// timezone — its time-of-day is documented as ignored (see the field's
	// doc comment), so a series whose StartsAt happens to carry a non-midnight
	// time still covers that same day's occurrence, not just later ones.
	sy, sm, sd := w.StartsAt.In(loc).Date()
	seriesStart := time.Date(sy, sm, sd, 0, 0, 0, 0, loc)
	if now.Before(seriesStart) {
		return time.Time{}, time.Time{}, false // the series hasn't begun yet
	}
	if !w.EndsAt.IsZero() && !now.Before(w.EndsAt) {
		return time.Time{}, time.Time{}, false // the series has stopped recurring
	}
	local := now.In(loc)
	for _, dayOffset := range [2]int{0, -1} {
		day := local.AddDate(0, 0, dayOffset)
		occDate := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
		if occDate.Before(seriesStart) {
			continue // this occurrence would predate the series itself
		}
		if !weekdayIn(w.Weekdays, day.Weekday()) {
			continue
		}
		occStart := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc)
		occEnd := occStart.Add(time.Duration(w.DurationMin) * time.Minute)
		if !now.Before(occStart) && now.Before(occEnd) {
			return occStart, occEnd, true
		}
	}
	return time.Time{}, time.Time{}, false
}

// MaxRecurringDurationMin bounds how long a single recurring occurrence may
// last. Enforced by callers that create/update a window (REST and MCP);
// recurringSpanAt's two-candidate-day lookback is only correct up to this
// bound — a longer occurrence could start further back than either candidate
// day covers and be missed.
const MaxRecurringDurationMin = 24 * 60

// Matches reports whether this window's SCOPE covers the given event —
// independent of whether it is currently Active. hostID/container/severity
// mirror the vocabulary AlertQuery already uses; project is the container's
// compose project (stack) name, resolved by the caller since the store has
// no Docker access of its own.
func (w MaintenanceWindow) Matches(hostID int64, project, container string, ruleID int64, severity string) bool {
	if len(w.HostIDs) > 0 && !containsInt64(w.HostIDs, hostID) {
		return false
	}
	if w.Project != "" && !containsFold(project, w.Project) {
		return false
	}
	if w.Container != "" && !containsFold(container, w.Container) {
		return false
	}
	if w.RuleID != nil && *w.RuleID != ruleID {
		return false
	}
	if len(w.Severities) > 0 && !containsStr(w.Severities, severity) {
		return false
	}
	return true
}

func parseTimeOfDay(s string) (hh, mm int, ok bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

func weekdayIn(days []time.Weekday, d time.Weekday) bool {
	for _, w := range days {
		if w == d {
			return true
		}
	}
	return false
}

func containsInt64(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsFold(haystack, needle string) bool {
	return needle != "" && strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// ListMaintenanceWindows returns every window, newest first, for the
// management UI.
func (s *Store) ListMaintenanceWindows(ctx context.Context) ([]MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mw.id, mw.name, mw.reason, mw.author_id, COALESCE(u.username, ''), mw.host_ids, mw.project, mw.container,
		       mw.rule_id, mw.severities, mw.recurring, mw.starts_at, mw.ends_at, mw.weekdays, mw.time_of_day,
		       mw.duration_min, mw.timezone, mw.ended, mw.created_at
		FROM maintenance_windows mw LEFT JOIN users u ON u.id = mw.author_id
		ORDER BY mw.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MaintenanceWindow{}
	for rows.Next() {
		w, err := scanMaintenanceWindow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// activeCandidateMaintenanceWindows returns every window that COULD be active
// at now — not yet manually ended, and (for a one-off window) not yet past
// its end time. Exact activeness (including a recurring window's schedule)
// is decided by MaintenanceWindow.Active, in Go, since SQL has no clean way
// to express "the day-of-week/time-of-day schedule covers this instant."
// Cheap at the scale of a single-instance panel's maintenance calendar.
func (s *Store) activeCandidateMaintenanceWindows(ctx context.Context, now time.Time) ([]MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, reason, author_id, '', host_ids, project, container, rule_id, severities,
		       recurring, starts_at, ends_at, weekdays, time_of_day, duration_min, timezone, ended, created_at
		FROM maintenance_windows
		WHERE ended = 0 AND (recurring = 1 OR ends_at > ?)`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MaintenanceWindow{}
	for rows.Next() {
		w, err := scanMaintenanceWindow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// FindActiveMaintenanceWindow returns the first window (if any) that is both
// Active at now and Matches the given event scope — the single call the
// alert engine needs to decide whether to suppress a delivery.
func (s *Store) FindActiveMaintenanceWindow(ctx context.Context, hostID int64, project, container string, ruleID int64, severity string, now time.Time) (*MaintenanceWindow, error) {
	// The monitor emits alert events with the LOCAL daemon's real seeded-row
	// id (whatever autoincrement gave it, commonly 1) — not 0, the alias every
	// other part of the app uses for "the local host". A window's own HostIDs
	// are normalized to that same 0 alias when it's created/updated (see the
	// REST/MCP handlers), so without normalizing here too, a window scoped to
	// "local" would silently never match a real local-host alert.
	hostID = s.NormalizeHostID(ctx, hostID)
	candidates, err := s.activeCandidateMaintenanceWindows(ctx, now)
	if err != nil {
		return nil, err
	}
	for _, w := range candidates {
		if w.Active(now) && w.Matches(hostID, project, container, ruleID, severity) {
			return &w, nil
		}
	}
	return nil, nil
}

// MaintenanceWindowByID looks up one window, for the update/end/delete
// handlers.
func (s *Store) MaintenanceWindowByID(ctx context.Context, id int64) (*MaintenanceWindow, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, reason, author_id, '', host_ids, project, container, rule_id, severities,
		       recurring, starts_at, ends_at, weekdays, time_of_day, duration_min, timezone, ended, created_at
		FROM maintenance_windows WHERE id = ?`, id)
	w, err := scanMaintenanceWindow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// CreateMaintenanceWindow records a new window.
func (s *Store) CreateMaintenanceWindow(ctx context.Context, w *MaintenanceWindow) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO maintenance_windows (name, reason, author_id, host_ids, project, container, rule_id, severities,
		                                  recurring, starts_at, ends_at, weekdays, time_of_day, duration_min, timezone,
		                                  ended, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		w.Name, w.Reason, w.AuthorID, marshalIDs(w.HostIDs), w.Project, w.Container, ruleIDArg(w.RuleID), marshalSections(w.Severities),
		boolToInt(w.Recurring), w.StartsAt.UTC().Format(time.RFC3339), formatOptionalTime(w.EndsAt),
		marshalWeekdays(w.Weekdays), w.TimeOfDay, w.DurationMin, w.Timezone,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMaintenanceWindow replaces a window's fields — everything except id,
// author and created_at, which describe who made it and when, not what it
// currently says.
func (s *Store) UpdateMaintenanceWindow(ctx context.Context, id int64, w *MaintenanceWindow) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE maintenance_windows SET name = ?, reason = ?, host_ids = ?, project = ?, container = ?, rule_id = ?,
		       severities = ?, recurring = ?, starts_at = ?, ends_at = ?, weekdays = ?, time_of_day = ?,
		       duration_min = ?, timezone = ?
		WHERE id = ?`,
		w.Name, w.Reason, marshalIDs(w.HostIDs), w.Project, w.Container, ruleIDArg(w.RuleID), marshalSections(w.Severities),
		boolToInt(w.Recurring), w.StartsAt.UTC().Format(time.RFC3339), formatOptionalTime(w.EndsAt),
		marshalWeekdays(w.Weekdays), w.TimeOfDay, w.DurationMin, w.Timezone, id)
	if err != nil {
		return err
	}
	return rowsAffectedOrNotFound(res)
}

// EndMaintenanceWindow stops a window early — the work finished ahead of
// schedule — without losing the row or its audit trail.
func (s *Store) EndMaintenanceWindow(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE maintenance_windows SET ended = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return rowsAffectedOrNotFound(res)
}

// DeleteMaintenanceWindow removes a window outright.
func (s *Store) DeleteMaintenanceWindow(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM maintenance_windows WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return rowsAffectedOrNotFound(res)
}

// PruneOldMaintenanceWindows deletes ONE-OFF windows whose EndsAt is more
// than olderThan in the past. Every successful deploy creates a new
// auto-silence window (see api.autoSilenceForDeploy) and nothing else ever
// removes them, so without this the table — and the list every caller
// re-authorizes against — grows without bound. Recurring windows are left
// alone: a recurring series has no single "it's over" moment the way a
// one-off window's EndsAt does.
func (s *Store) PruneOldMaintenanceWindows(ctx context.Context, olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan).UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM maintenance_windows
		WHERE recurring = 0 AND ends_at != '' AND ends_at < ?`, cutoff)
	return err
}

func rowsAffectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanMaintenanceWindow(r scanner) (*MaintenanceWindow, error) {
	var w MaintenanceWindow
	var hostIDs, severities, weekdays, endsAt, created, startsAt string
	var ruleID sql.NullInt64
	var recurring, ended int
	if err := r.Scan(&w.ID, &w.Name, &w.Reason, &w.AuthorID, &w.Author, &hostIDs, &w.Project, &w.Container,
		&ruleID, &severities, &recurring, &startsAt, &endsAt, &weekdays, &w.TimeOfDay, &w.DurationMin, &w.Timezone,
		&ended, &created); err != nil {
		return nil, err
	}
	w.HostIDs = unmarshalIDs(hostIDs)
	w.Severities = unmarshalSections(severities)
	w.Recurring = recurring != 0
	w.Ended = ended != 0
	w.StartsAt, _ = time.Parse(time.RFC3339, startsAt)
	if endsAt != "" {
		w.EndsAt, _ = time.Parse(time.RFC3339, endsAt)
	}
	w.CreatedAt, _ = time.Parse(time.RFC3339, created)
	w.Weekdays = unmarshalWeekdays(weekdays)
	if ruleID.Valid {
		id := ruleID.Int64
		w.RuleID = &id
	}
	return &w, nil
}

func ruleIDArg(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func marshalWeekdays(days []time.Weekday) string {
	if len(days) == 0 {
		return ""
	}
	ints := make([]int, len(days))
	for i, d := range days {
		ints[i] = int(d)
	}
	b, _ := json.Marshal(ints)
	return string(b)
}

func unmarshalWeekdays(raw string) []time.Weekday {
	if raw == "" {
		return nil
	}
	var ints []int
	if err := json.Unmarshal([]byte(raw), &ints); err != nil {
		return nil
	}
	out := make([]time.Weekday, len(ints))
	for i, v := range ints {
		out[i] = time.Weekday(v)
	}
	return out
}
