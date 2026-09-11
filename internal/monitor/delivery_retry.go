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

	// deliveryRetryWebhookTimeout mirrors dispatch()'s own per-attempt budget
	// (webhook.go) — the retry sweep drives the identical attempt() call and
	// must bound it the same way.
	deliveryRetryWebhookTimeout = 12 * time.Second
	// deliveryRetryDBTimeout bounds each individual store call the sweep
	// makes AROUND a send attempt (loading the event, checking for an active
	// maintenance window, recording the outcome). Each of these gets its OWN
	// fresh context rather than sharing one across the whole retryOne call —
	// sharing one meant a send that used most of its own budget left almost
	// nothing for the bookkeeping call needed right after it, which could
	// then silently fail to record what just happened.
	deliveryRetryDBTimeout = 5 * time.Second
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
	listCtx, cancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
	due, err := m.store.DueAlertDeliveryRetries(listCtx, time.Now(), retrySweepBatch)
	cancel()
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
//
// Every store call below gets its OWN short-lived context (deliveryRetryDBTimeout),
// and the send attempt gets its own (channel-appropriate) one — none of them
// share a budget. Sharing one context across the whole call used to mean a
// send that consumed most of its own timeout left almost nothing for the
// bookkeeping call needed right after it, which could then silently fail to
// record what had just happened; ctx itself (the sweep loop's long-lived
// context) is only the parent each of these derives from, for shutdown to
// still cancel an in-flight retry.
func (m *Monitor) retryOne(ctx context.Context, r store.AlertDeliveryRetry) {
	loadCtx, loadCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
	ev, err := m.store.AlertEventByID(loadCtx, r.EventID)
	loadCancel()
	if err != nil {
		// The event itself is gone (or unreadable) — nothing left to retry
		// against.
		delCtx, delCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
		if err := m.store.DeleteAlertDeliveryRetry(delCtx, r.ID); err != nil {
			log.Printf("monitor: delete orphaned delivery retry: %v", err)
		}
		delCancel()
		return
	}

	// A maintenance window may have STARTED after the original failure, or
	// still be running — a retry must respect it too, or "silence" would
	// have a hole a failed-then-retried delivery slips through. ev.Project
	// is the compose project the live alert path resolved when the event
	// first fired, persisted specifically so this later read can still use
	// it — the live resolution itself (Docker event attributes,
	// ListContainers labels, the stats snapshot) has nothing left to consult
	// by retry time.
	winCtx, winCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
	win, werr := m.store.FindActiveMaintenanceWindow(winCtx, ev.HostID, ev.Project, ev.ContainerName, ev.RuleID, ev.Severity, time.Now())
	winCancel()
	if werr != nil {
		log.Printf("monitor: check maintenance window for delivery retry: %v", werr)
	} else if win != nil {
		// Not an attempt — don't burn down the retry budget for a send this
		// deliberately chose not to make. Just wait the window out and check
		// again on the same cadence.
		rescheduleCtx, rescheduleCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
		if err := m.store.RescheduleAlertDeliveryRetry(rescheduleCtx, r.ID, false, time.Now().Add(backoffFor(r.Attempt)), r.LastError); err != nil {
			log.Printf("monitor: reschedule suppressed delivery retry: %v", err)
		}
		rescheduleCancel()
		return
	}

	var ok, retriable bool
	var detail string
	switch {
	case r.Channel == "webhook" && r.WebhookID != nil:
		sendCtx, sendCancel := context.WithTimeout(ctx, deliveryRetryWebhookTimeout)
		var status int
		var target string
		ok, status, target, detail, retriable = m.dispatcher.attempt(sendCtx, *r.WebhookID, ev)
		sendCancel()
		recordCtx, recordCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
		m.dispatcher.record(recordCtx, ev.ID, target, ok, status, detail)
		recordCancel()
	case r.Channel == "email":
		sendCtx, sendCancel := context.WithTimeout(ctx, emailSendTimeout)
		var target string
		ok, target, detail, retriable = m.attemptEmail(sendCtx, ev, r.RuleEmails)
		sendCancel()
		recordCtx, recordCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
		m.recordDelivery(recordCtx, ev.ID, target, ok, detail)
		recordCancel()
	default:
		// An unrecognised or malformed row (e.g. a webhook retry with no
		// webhook id) — nothing sane to retry.
		delCtx, delCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
		_ = m.store.DeleteAlertDeliveryRetry(delCtx, r.ID)
		delCancel()
		return
	}

	finalCtx, finalCancel := context.WithTimeout(ctx, deliveryRetryDBTimeout)
	defer finalCancel()
	switch {
	case ok:
		_ = m.store.DeleteAlertDeliveryRetry(finalCtx, r.ID)
	case !retriable || r.Attempt+1 >= maxDeliveryRetries:
		log.Printf("monitor: giving up on delivery retry for event %d after %d attempt(s): %s", r.EventID, r.Attempt+1, detail)
		_ = m.store.DeleteAlertDeliveryRetry(finalCtx, r.ID)
	default:
		if err := m.store.RescheduleAlertDeliveryRetry(finalCtx, r.ID, true, time.Now().Add(backoffFor(r.Attempt+1)), detail); err != nil {
			log.Printf("monitor: reschedule delivery retry: %v", err)
		}
	}
}
