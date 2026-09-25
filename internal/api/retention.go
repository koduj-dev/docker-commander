package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

const (
	retentionFirstRunDelay = 2 * time.Minute
	retentionInterval      = 24 * time.Hour
	retentionRunTimeout    = 10 * time.Minute
)

// StartRetentionLoop purges history older than the configured retention once a
// day (first run shortly after startup, so a restart-heavy install still purges)
// until ctx is cancelled. What it deleted is logged and stored (see
// store.RetentionRun) so nobody has to wonder where old alerts went.
func (s *Server) StartRetentionLoop(ctx context.Context) {
	timer := time.NewTimer(retentionFirstRunDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			rctx, cancel := context.WithTimeout(ctx, retentionRunTimeout)
			if _, err := s.runRetention(rctx, "scheduled", nil); err != nil && !errors.Is(err, errRetentionBusy) {
				log.Printf("retention: %v", err)
			}
			cancel()
			timer.Reset(retentionInterval)
		}
	}
}

var errRetentionBusy = errors.New("a purge is already running")

// runRetention applies the stored policy once, records the outcome and returns
// it. actor is the admin who asked for a manual run (nil for the scheduler).
func (s *Server) runRetention(ctx context.Context, trigger string, actor *auth.Claims) (*store.RetentionRun, error) {
	if !s.retentionMu.TryLock() {
		return nil, errRetentionBusy
	}
	defer s.retentionMu.Unlock()

	policy, err := s.store.Retention(ctx)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	run := &store.RetentionRun{At: start.UTC(), Trigger: trigger}
	run.DBBytesBefore, _, _ = s.store.DBSize(ctx)

	// Each area is independent: one failing must not stop the others from being
	// purged, and the error is reported rather than swallowed.
	var errs []error
	note := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	var derr error
	run.AlertEvents, run.AlertDeliveries, derr = s.store.PurgeAlertEvents(ctx, policy.AlertEventsDays, start)
	note(derr)
	n, derr := s.store.PurgeAlertDeliveries(ctx, policy.AlertDeliveriesDays, start)
	run.AlertDeliveries += n
	note(derr)
	run.Audit, derr = s.store.PurgeAudit(ctx, policy.AuditDays, start)
	note(derr)

	refs, derr := s.store.TrimRevisions(ctx, policy.RevisionsKeep)
	note(derr)
	run.Revisions = int64(len(refs))
	for _, ref := range refs {
		// Rows are already gone; a missing file is fine, any other failure is only
		// logged — the row can't be restored and the file is now unreachable.
		if err := os.Remove(s.revisionZipPath(ref.ProjectID, ref.Revision)); err == nil {
			run.RevisionFiles++
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("retention: remove snapshot project %d rev %d: %v", ref.ProjectID, ref.Revision, err)
		}
	}

	run.DBBytesAfter, _, _ = s.store.DBSize(ctx)
	run.DurationMs = time.Since(start).Milliseconds()
	if err := errors.Join(errs...); err != nil {
		run.Error = err.Error()
	}

	log.Printf("retention: %s purge deleted alert_events=%d alert_deliveries=%d audit=%d revisions=%d (files=%d) in %dms, db %d -> %d bytes%s",
		trigger, run.AlertEvents, run.AlertDeliveries, run.Audit, run.Revisions, run.RevisionFiles,
		run.DurationMs, run.DBBytesBefore, run.DBBytesAfter, errSuffix(run.Error))

	// The record is written on a fresh context: the run's own may have expired
	// mid-purge, which is exactly when the record matters most.
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.SaveRetentionRun(wctx, *run); err != nil {
		log.Printf("retention: record run: %v", err)
	}
	// Only a purge that removed something is worth an audit entry; a daily
	// "deleted nothing" line would just be noise in the log it protects.
	if run.Total() > 0 || run.Error != "" {
		entry := store.AuditEntry{Action: "retention.purge", Username: "system", Detail: trigger}
		if actor != nil {
			entry.UserID, entry.Username = actor.UserID, actor.Username
		}
		_ = s.store.Audit(wctx, entry)
	}
	if run.Error != "" {
		return run, errors.New(run.Error)
	}
	return run, nil
}

func errSuffix(msg string) string {
	if msg == "" {
		return ""
	}
	return " — errors: " + msg
}

// retentionLimits are the bounds the UI enforces on its inputs; the server
// enforces the same in RetentionPolicy.Validate.
func retentionLimits() map[string]int {
	return map[string]int{
		"minAuditDays": store.MinAuditRetentionDays, "minAlertDays": store.MinAlertRetentionDays,
		"minRevisionsKeep": store.MinRevisionsKeep, "maxDays": store.MaxRetentionDays,
	}
}

func (s *Server) handleGetRetention(w http.ResponseWriter, r *http.Request) {
	policy, err := s.store.Retention(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read the retention policy")
		return
	}
	stats, err := s.store.RetentionStats(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read database statistics")
		return
	}
	last, _ := s.store.LastRetentionRun(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"policy": policy, "defaults": store.DefaultRetention(), "limits": retentionLimits(),
		"stats": stats, "lastRun": last,
	})
}

func (s *Server) handleSetRetention(w http.ResponseWriter, r *http.Request) {
	var p store.RetentionPolicy
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := p.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetRetention(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save the retention policy")
		return
	}
	s.audit(r, "retention.update", "", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePurgeRetention runs the purge now, against the SAVED policy (not
// whatever is typed into the form), and returns what it did.
func (s *Server) handlePurgeRetention(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), retentionRunTimeout)
	defer cancel()
	run, err := s.runRetention(ctx, "manual", claims)
	if errors.Is(err, errRetentionBusy) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if run == nil {
		writeErr(w, http.StatusInternalServerError, "purge failed")
		return
	}
	writeJSON(w, http.StatusOK, run)
}
