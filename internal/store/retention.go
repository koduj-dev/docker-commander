package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RetentionPolicy says how long each kind of history is kept. It exists because
// nothing else ever deletes the alert feed, its delivery log, the audit log or
// project revision snapshots — on a busy host they only grow.
//
// A zero value means "keep forever" for that area, so an operator who wants the
// old behaviour can have it explicitly. The defaults (DefaultRetention) are ON.
type RetentionPolicy struct {
	// AlertEventsDays / AlertDeliveriesDays are TTLs in days. A delivery record
	// belongs to an alert event, so it cannot outlive it (see Validate).
	AlertEventsDays     int `json:"alertEventsDays"`
	AlertDeliveriesDays int `json:"alertDeliveriesDays"`
	// AuditDays is the audit log TTL. Never below MinAuditRetentionDays: the log
	// is what an incident review reads, and a short TTL would let a mistake (or an
	// attacker's tidy-up) erase it within days.
	AuditDays int `json:"auditDays"`
	// RevisionsKeep is how many project revisions (and their on-disk snapshots)
	// are kept per project — a count, not an age, since a project deployed once a
	// year still wants its last few revisions.
	RevisionsKeep int `json:"revisionsKeep"`
}

const (
	MinAuditRetentionDays = 30
	MinAlertRetentionDays = 1
	MinRevisionsKeep      = 3
	// Upper bound on any TTL, so a typo can't overflow the cutoff arithmetic.
	MaxRetentionDays = 36500

	retentionSettingKey     = "retention.policy"
	retentionLastRunSetting = "retention.lastRun"
)

// DefaultRetention is what an install gets until an admin changes it.
func DefaultRetention() RetentionPolicy {
	return RetentionPolicy{AlertEventsDays: 90, AlertDeliveriesDays: 90, AuditDays: 365, RevisionsKeep: 50}
}

// Validate rejects a policy the purge job must never act on.
func (p RetentionPolicy) Validate() error {
	days := func(name string, v, min int) error {
		if v == 0 {
			return nil // keep forever
		}
		if v < min || v > MaxRetentionDays {
			return fmt.Errorf("%s must be 0 (keep forever) or between %d and %d days", name, min, MaxRetentionDays)
		}
		return nil
	}
	if err := days("alert events", p.AlertEventsDays, MinAlertRetentionDays); err != nil {
		return err
	}
	if err := days("alert deliveries", p.AlertDeliveriesDays, MinAlertRetentionDays); err != nil {
		return err
	}
	if err := days("audit log", p.AuditDays, MinAuditRetentionDays); err != nil {
		return err
	}
	if p.RevisionsKeep != 0 && p.RevisionsKeep < MinRevisionsKeep {
		return fmt.Errorf("revisions kept per project must be 0 (unlimited) or at least %d", MinRevisionsKeep)
	}
	// A delivery record is deleted with its event, so a longer delivery TTL would
	// silently be shorter than what the page says.
	if p.AlertEventsDays != 0 && (p.AlertDeliveriesDays == 0 || p.AlertDeliveriesDays > p.AlertEventsDays) {
		return errors.New("alert deliveries cannot be kept longer than the alert events they belong to")
	}
	return nil
}

// Retention returns the stored policy, or the defaults when none was saved. An
// unreadable stored value falls back to the defaults too — a corrupt setting
// must not switch the purge off (or on, more aggressively) by accident.
func (s *Store) Retention(ctx context.Context) (RetentionPolicy, error) {
	raw, err := s.Setting(ctx, retentionSettingKey)
	if err != nil {
		return DefaultRetention(), err
	}
	if raw == "" {
		return DefaultRetention(), nil
	}
	var p RetentionPolicy
	if json.Unmarshal([]byte(raw), &p) != nil || p.Validate() != nil {
		return DefaultRetention(), nil
	}
	return p, nil
}

// SetRetention validates and stores the policy.
func (s *Store) SetRetention(ctx context.Context, p RetentionPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(p)
	return s.SetSetting(ctx, retentionSettingKey, string(b))
}

// RetentionRun is the outcome of one purge, kept so the settings page can say
// what the last one did without anyone reading the log.
type RetentionRun struct {
	At         time.Time `json:"at"`
	Trigger    string    `json:"trigger"` // "scheduled" | "manual"
	DurationMs int64     `json:"durationMs"`
	// Deleted counts rows per area; RevisionFiles counts snapshot files removed.
	AlertEvents     int64  `json:"alertEvents"`
	AlertDeliveries int64  `json:"alertDeliveries"`
	Audit           int64  `json:"audit"`
	Revisions       int64  `json:"revisions"`
	RevisionFiles   int64  `json:"revisionFiles"`
	DBBytesBefore   int64  `json:"dbBytesBefore"`
	DBBytesAfter    int64  `json:"dbBytesAfter"`
	Error           string `json:"error,omitempty"`
}

// Total is the number of rows the run deleted.
func (r RetentionRun) Total() int64 {
	return r.AlertEvents + r.AlertDeliveries + r.Audit + r.Revisions
}

// LastRetentionRun returns the most recent recorded purge, or nil.
func (s *Store) LastRetentionRun(ctx context.Context) (*RetentionRun, error) {
	raw, err := s.Setting(ctx, retentionLastRunSetting)
	if err != nil || raw == "" {
		return nil, err
	}
	var r RetentionRun
	if json.Unmarshal([]byte(raw), &r) != nil {
		return nil, nil
	}
	return &r, nil
}

// SaveRetentionRun records the outcome of a purge.
func (s *Store) SaveRetentionRun(ctx context.Context, r RetentionRun) error {
	b, _ := json.Marshal(r)
	return s.SetSetting(ctx, retentionLastRunSetting, string(b))
}

// purgeBatch is how many rows one DELETE removes. The pool is a single
// connection, so one giant DELETE would stall every request behind it; small
// batches let other work interleave between them.
const purgeBatch = 2000

// deleteBatched runs a "DELETE ... LIMIT-style" statement until it stops
// deleting rows or ctx is done, and returns the total. stmt must delete at most
// purgeBatch rows per execution.
func (s *Store) deleteBatched(ctx context.Context, stmt string, args ...any) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := s.db.ExecContext(ctx, stmt, args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < purgeBatch {
			return total, nil
		}
	}
}

func cutoffFor(days int, now time.Time) string {
	return now.Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
}

// PurgeAlertEvents deletes alert events older than days, and anything that only
// made sense with them: their delivery records and queued retries. Orphans
// (rows whose event is already gone) are swept too, so the tables converge even
// after an interrupted run.
func (s *Store) PurgeAlertEvents(ctx context.Context, days int, now time.Time) (events, deliveries int64, err error) {
	if days <= 0 {
		return 0, 0, nil
	}
	events, err = s.deleteBatched(ctx,
		`DELETE FROM alert_events WHERE id IN (SELECT id FROM alert_events WHERE created_at < ? LIMIT `+fmt.Sprint(purgeBatch)+`)`,
		cutoffFor(days, now))
	if err != nil {
		return events, 0, err
	}
	deliveries, err = s.deleteBatched(ctx,
		`DELETE FROM alert_deliveries WHERE id IN (
			SELECT d.id FROM alert_deliveries d LEFT JOIN alert_events e ON e.id = d.event_id
			WHERE e.id IS NULL LIMIT `+fmt.Sprint(purgeBatch)+`)`)
	if err != nil {
		return events, deliveries, err
	}
	// A retry for an event that no longer exists can never be delivered.
	_, err = s.deleteBatched(ctx,
		`DELETE FROM alert_delivery_retries WHERE id IN (
			SELECT r.id FROM alert_delivery_retries r LEFT JOIN alert_events e ON e.id = r.event_id
			WHERE e.id IS NULL LIMIT `+fmt.Sprint(purgeBatch)+`)`)
	return events, deliveries, err
}

// PurgeAlertDeliveries deletes delivery records older than days.
func (s *Store) PurgeAlertDeliveries(ctx context.Context, days int, now time.Time) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	return s.deleteBatched(ctx,
		`DELETE FROM alert_deliveries WHERE id IN (SELECT id FROM alert_deliveries WHERE attempted_at < ? LIMIT `+fmt.Sprint(purgeBatch)+`)`,
		cutoffFor(days, now))
}

// PurgeAudit deletes audit entries older than days. Anything shorter than
// MinAuditRetentionDays is refused here as well as in Validate, so no caller —
// present or future — can wipe recent history through this method.
func (s *Store) PurgeAudit(ctx context.Context, days int, now time.Time) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	if days < MinAuditRetentionDays {
		return 0, fmt.Errorf("audit retention below %d days refused", MinAuditRetentionDays)
	}
	return s.deleteBatched(ctx,
		`DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE created_at < ? LIMIT `+fmt.Sprint(purgeBatch)+`)`,
		cutoffFor(days, now))
}

// RevisionRef names a revision snapshot that was trimmed, so the caller can
// remove its file from disk.
type RevisionRef struct {
	ProjectID int64
	Revision  int
}

// TrimRevisions keeps the newest keep revisions of every project and deletes the
// rest, returning what it deleted (the caller removes the snapshot files after
// the rows are gone, so a failed file removal can never leave a row pointing at
// a missing file).
func (s *Store) TrimRevisions(ctx context.Context, keep int) ([]RevisionRef, error) {
	if keep <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.project_id, r.revision FROM project_revisions r
		WHERE (SELECT COUNT(*) FROM project_revisions n WHERE n.project_id = r.project_id AND n.revision > r.revision) >= ?`, keep)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var refs []RevisionRef
	for rows.Next() {
		var id int64
		var ref RevisionRef
		if err := rows.Scan(&id, &ref.ProjectID, &ref.Revision); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM project_revisions WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// RetentionArea is the size of one purgeable area.
type RetentionArea struct {
	Rows   int64  `json:"rows"`
	Oldest string `json:"oldest,omitempty"` // RFC3339; empty when the area is empty
}

// RetentionStats is what the settings page shows next to the policy.
type RetentionStats struct {
	AlertEvents     RetentionArea `json:"alertEvents"`
	AlertDeliveries RetentionArea `json:"alertDeliveries"`
	Audit           RetentionArea `json:"audit"`
	Revisions       RetentionArea `json:"revisions"`
	DBBytes         int64         `json:"dbBytes"`
	// DBFreeBytes is space inside the file that deleted rows left behind. SQLite
	// reuses it for new rows but does not shrink the file, so a purge lowers this
	// number's "used" share, not the file size itself.
	DBFreeBytes int64 `json:"dbFreeBytes"`
}

func (s *Store) area(ctx context.Context, table, col string) (RetentionArea, error) {
	var a RetentionArea
	var oldest *string
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(`+col+`) FROM `+table).Scan(&a.Rows, &oldest); err != nil {
		return a, err
	}
	if oldest != nil {
		a.Oldest = *oldest
	}
	return a, nil
}

// DBSize returns the database size in bytes and how much of it is free pages.
func (s *Store) DBSize(ctx context.Context) (total, free int64, err error) {
	var pages, pageSize, freePages int64
	if err = s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return
	}
	if err = s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return
	}
	if err = s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return
	}
	return pages * pageSize, freePages * pageSize, nil
}

// RetentionStats counts each purgeable area and reports the database size.
func (s *Store) RetentionStats(ctx context.Context) (RetentionStats, error) {
	var out RetentionStats
	var err error
	if out.AlertEvents, err = s.area(ctx, "alert_events", "created_at"); err != nil {
		return out, err
	}
	if out.AlertDeliveries, err = s.area(ctx, "alert_deliveries", "attempted_at"); err != nil {
		return out, err
	}
	if out.Audit, err = s.area(ctx, "audit_log", "created_at"); err != nil {
		return out, err
	}
	if out.Revisions, err = s.area(ctx, "project_revisions", "created_at"); err != nil {
		return out, err
	}
	out.DBBytes, out.DBFreeBytes, err = s.DBSize(ctx)
	return out, err
}
