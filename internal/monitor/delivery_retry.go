package monitor

import (
	"context"
	"log"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

const (
	// maxDeliveryRetries is how many retry attempts a failed delivery gets
	// (on top of its original attempt) before this gives up on it.
	maxDeliveryRetries = 5
	deliveryRetryBase  = time.Minute
	// deliveryRetryMaxGap caps the exponential backoff so a long-broken
	// endpoint doesn't stretch the checks out indefinitely.
	deliveryRetryMaxGap = 30 * time.Minute

	retrySweepInterval = 30 * time.Second
	// retrySweepBatch bounds one sweep tick's work so a large backlog (e.g.
	// after an extended outage) can't make a single tick run unbounded.
	retrySweepBatch = 20
)

// backoffFor returns how long to wait before the NEXT attempt, given attempt
// retries have already happened (0 before the first retry).
func backoffFor(attempt int) time.Duration {
	if attempt < 0 || attempt > 20 { // guard the shift against nonsense input
		return deliveryRetryMaxGap
	}
	d := deliveryRetryBase << attempt
	if d <= 0 || d > deliveryRetryMaxGap {
		return deliveryRetryMaxGap
	}
	return d
}

// enqueueRetry queues a webhook delivery's first retry. Called only for a
// failure attempt() already classified as retriable.
func (d *dispatcher) enqueueRetry(ctx context.Context, eventID, webhookID int64, lastError string) {
	if err := d.store.EnqueueAlertDeliveryRetry(ctx, &store.AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &webhookID,
		NextAttemptAt: time.Now().Add(backoffFor(0)), LastError: lastError,
	}); err != nil {
		log.Printf("monitor: enqueue webhook delivery retry: %v", err)
	}
}

// enqueueEmailRetry is enqueueRetry's email twin.
func (m *Monitor) enqueueEmailRetry(ctx context.Context, eventID int64, ruleEmails []string, lastError string) {
	if err := m.store.EnqueueAlertDeliveryRetry(ctx, &store.AlertDeliveryRetry{
		EventID: eventID, Channel: "email", RuleEmails: ruleEmails,
		NextAttemptAt: time.Now().Add(backoffFor(0)), LastError: lastError,
	}); err != nil {
		log.Printf("monitor: enqueue email delivery retry: %v", err)
	}
}

// retrySweepLoop periodically retries queued failed deliveries.
func (m *Monitor) retrySweepLoop(ctx context.Context) {
	t := time.NewTicker(retrySweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweepDeliveryRetries(ctx)
		}
	}
}

func (m *Monitor) sweepDeliveryRetries(ctx context.Context) {
	due, err := m.store.DueAlertDeliveryRetries(ctx, time.Now(), retrySweepBatch)
	if err != nil {
		log.Printf("monitor: list due delivery retries: %v", err)
		return
	}
	for _, r := range due {
		m.retryOne(ctx, r)
	}
}

// retryOne drives one queued retry to its next outcome: delivered (delete
// the queue row), still failing but with attempts left (reschedule, further
// out), or done trying (delete, having given up).
func (m *Monitor) retryOne(ctx context.Context, r store.AlertDeliveryRetry) {
	ev, err := m.store.AlertEventByID(ctx, r.EventID)
	if err != nil {
		// The event itself is gone (or unreadable) — nothing left to retry
		// against.
		if err := m.store.DeleteAlertDeliveryRetry(ctx, r.ID); err != nil {
			log.Printf("monitor: delete orphaned delivery retry: %v", err)
		}
		return
	}

	// A maintenance window may have STARTED after the original failure, or
	// still be running — a retry must respect it too, or "silence" would
	// have a hole a failed-then-retried delivery slips through. Reduced
	// scope on purpose: AlertEvent doesn't persist the container's compose
	// project (only the live alert path resolves that), so a window scoped
	// ONLY by project won't suppress a retry here. The safe direction to be
	// wrong in is "retries a bit too eagerly," never "silently swallows a
	// delivery forever."
	if win, werr := m.store.FindActiveMaintenanceWindow(ctx, ev.HostID, "", ev.ContainerName, ev.RuleID, ev.Severity, time.Now()); werr != nil {
		log.Printf("monitor: check maintenance window for delivery retry: %v", werr)
	} else if win != nil {
		// Not an attempt — don't burn down the retry budget for a send this
		// deliberately chose not to make. Just wait the window out and check
		// again on the same cadence.
		if err := m.store.RescheduleAlertDeliveryRetry(ctx, r.ID, false, time.Now().Add(backoffFor(r.Attempt)), r.LastError); err != nil {
			log.Printf("monitor: reschedule suppressed delivery retry: %v", err)
		}
		return
	}

	var ok, retriable bool
	var detail string
	switch {
	case r.Channel == "webhook" && r.WebhookID != nil:
		var status int
		var target string
		ok, status, target, detail, retriable = m.dispatcher.attempt(ctx, *r.WebhookID, ev)
		m.dispatcher.record(ctx, ev.ID, target, ok, status, detail)
	case r.Channel == "email":
		var target string
		ok, target, detail, retriable = m.attemptEmail(ctx, ev, r.RuleEmails)
		m.recordDelivery(ctx, ev.ID, target, ok, detail)
	default:
		// An unrecognised or malformed row (e.g. a webhook retry with no
		// webhook id) — nothing sane to retry.
		_ = m.store.DeleteAlertDeliveryRetry(ctx, r.ID)
		return
	}

	switch {
	case ok:
		_ = m.store.DeleteAlertDeliveryRetry(ctx, r.ID)
	case !retriable || r.Attempt+1 >= maxDeliveryRetries:
		log.Printf("monitor: giving up on delivery retry for event %d after %d attempt(s): %s", r.EventID, r.Attempt+1, detail)
		_ = m.store.DeleteAlertDeliveryRetry(ctx, r.ID)
	default:
		if err := m.store.RescheduleAlertDeliveryRetry(ctx, r.ID, true, time.Now().Add(backoffFor(r.Attempt+1)), detail); err != nil {
			log.Printf("monitor: reschedule delivery retry: %v", err)
		}
	}
}
