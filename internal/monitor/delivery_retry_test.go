package monitor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestWebhookRetriableFailureIsEnqueued is the point of the feature: a
// webhook returning 500 (the endpoint's own "try later") gets a queued
// retry, not just a recorded failure nobody follows up on.
func TestWebhookRetriableFailureIsEnqueued(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}

	eventID, err := st.InsertAlertEvent(ctx, &store.AlertEvent{RuleName: "r", ContainerName: "web-1", Message: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	m.dispatcher.dispatch(whID, &store.AlertEvent{ID: eventID, RuleName: "r", ContainerName: "web-1", Message: "boom"})

	waitForDeliveryRow(t, st, eventID)
	rows, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Channel != "webhook" || rows[0].WebhookID == nil || *rows[0].WebhookID != whID {
		t.Fatalf("expected one queued webhook retry, got %+v", rows)
	}
}

// TestNonRetriableWebhookFailureIsNotEnqueued is the other half: a 4xx (a
// configuration problem — bad payload, bad auth) must not be retried
// indefinitely against an endpoint that will keep refusing it.
func TestNonRetriableWebhookFailureIsNotEnqueued(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}

	eventID, err := st.InsertAlertEvent(ctx, &store.AlertEvent{RuleName: "r", ContainerName: "web-1", Message: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	m.dispatcher.dispatch(whID, &store.AlertEvent{ID: eventID, RuleName: "r", ContainerName: "web-1", Message: "boom"})

	waitForDeliveryRow(t, st, eventID)
	rows, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a 400 must not be queued for retry, got %+v", rows)
	}
}

// TestSweepRetriesAWebhookUntilItSucceeds proves the sweep actually resends
// — not just tracks — a queued retry, and stops once it succeeds.
func TestSweepRetriesAWebhookUntilItSucceeds(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	var calls int
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.InsertAlertEvent(ctx, &store.AlertEvent{RuleName: "r", ContainerName: "web-1", Message: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueAlertDeliveryRetry(ctx, &store.AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &whID, NextAttemptAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	m.sweepDeliveryRetries(ctx)

	if calls != 1 {
		t.Fatalf("expected exactly one retry attempt so far, got %d calls", calls)
	}
	rows, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(rows) != 1 || rows[0].Attempt != 1 {
		t.Fatalf("after a failed retry it should still be queued with attempt=1: %+v err=%v", rows, err)
	}

	// Force it due again (real code waits out the backoff; the test doesn't
	// need to) and sweep once more — this time the endpoint succeeds.
	if err := st.RescheduleAlertDeliveryRetry(ctx, rows[0].ID, false, time.Now().Add(-time.Second), rows[0].LastError); err != nil {
		t.Fatal(err)
	}
	m.sweepDeliveryRetries(ctx)

	if calls != 2 {
		t.Fatalf("expected a second retry attempt, got %d calls", calls)
	}
	after, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("a successful retry should remove the queue row, got %+v", after)
	}
	deliveries, err := st.AlertDeliveriesFor(ctx, []int64{eventID})
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, d := range deliveries[eventID] {
		if d.OK {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("expected a successful delivery recorded, got %+v", deliveries[eventID])
	}
}

// TestSweepGivesUpAfterMaxRetries proves retrying is bounded — an endpoint
// that never recovers must not be hammered forever.
func TestSweepGivesUpAfterMaxRetries(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	var calls int
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.InsertAlertEvent(ctx, &store.AlertEvent{RuleName: "r", ContainerName: "web-1", Message: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueAlertDeliveryRetry(ctx, &store.AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &whID, NextAttemptAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < maxDeliveryRetries; i++ {
		rows, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(time.Hour), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("iteration %d: expected the retry still queued, got %d rows", i, len(rows))
		}
		if err := st.RescheduleAlertDeliveryRetry(ctx, rows[0].ID, false, time.Now().Add(-time.Second), rows[0].LastError); err != nil {
			t.Fatal(err)
		}
		m.sweepDeliveryRetries(ctx)
	}

	if calls != maxDeliveryRetries {
		t.Fatalf("expected exactly %d attempts, got %d", maxDeliveryRetries, calls)
	}
	rows, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("after exhausting retries the queue row should be gone, got %+v err=%v", rows, err)
	}
}

// TestSweepSkipsButDoesNotBurnAttemptWhenWindowActive is the maintenance
// window interaction: a window that started AFTER the original failure (or
// is still running) must also suppress a retry, but skipping is not a
// failed attempt — it must not count against the retry budget or record a
// delivery that never happened.
func TestSweepSkipsButDoesNotBurnAttemptWhenWindowActive(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("webhook must not be called while a covering maintenance window is active")
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.InsertAlertEvent(ctx, &store.AlertEvent{
		RuleName: "r", ContainerName: "web-1", Message: "boom", HostID: 1, Severity: "critical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "upgrade", Reason: "planned", HostIDs: []int64{1},
		StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueAlertDeliveryRetry(ctx, &store.AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &whID, NextAttemptAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	m.sweepDeliveryRetries(ctx)

	rows, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(rows) != 1 || rows[0].Attempt != 0 {
		t.Fatalf("a suppressed retry should stay queued with attempt unchanged: %+v err=%v", rows, err)
	}
	deliveries, err := st.AlertDeliveriesFor(ctx, []int64{eventID})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries[eventID]) != 0 {
		t.Fatalf("a skipped-for-suppression retry must not record a delivery attempt: %+v", deliveries[eventID])
	}
}

// TestAttemptEmailUnconfiguredSMTPIsNotRetriable pins the classification
// that matters most for email: an unconfigured server (or no resolvable
// recipient) is a standing configuration problem, not a transient failure,
// so it must never enter the retry queue.
func TestAttemptEmailUnconfiguredSMTPIsNotRetriable(t *testing.T) {
	m, _, ctx := newMaintenanceMonitor(t)
	ok, _, _, retriable := m.attemptEmail(ctx, &store.AlertEvent{RuleName: "r"}, nil)
	if ok {
		t.Fatal("no SMTP configured should not report success")
	}
	if retriable {
		t.Fatal("no SMTP configured is a config problem, not transient — must not be retriable")
	}
}

// waitForDeliveryRow polls briefly for the async dispatch goroutine to have
// recorded its outcome, so the test isn't racing it.
func waitForDeliveryRow(t *testing.T, st *store.Store, eventID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		deliveries, err := st.AlertDeliveriesFor(context.Background(), []int64{eventID})
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries[eventID]) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the delivery attempt to be recorded")
}
