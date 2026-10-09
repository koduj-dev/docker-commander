package monitor

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// TestSweepRespectsAProjectOnlyMaintenanceWindowForARetry is the fix for a
// real bug: the retry lookup used to pass an empty project unconditionally,
// so a window scoped ONLY by project (no host/rule/severity restriction —
// exactly what auto-silence-after-deploy windows look like) could never
// suppress a retry, even though the original live alert resolved and
// persisted that same project. The concrete scenario: a webhook fails, a
// project-scoped deploy grace window starts before the retry is due — the
// retry must wait it out, not send.
func TestSweepRespectsAProjectOnlyMaintenanceWindowForARetry(t *testing.T) {
	m, st, ctx := newMaintenanceMonitor(t)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("webhook must not be called: a project-only window covers this event's persisted project")
	}))
	defer recv.Close()
	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	// The project is part of the ORIGINAL event, exactly as the live alert
	// path would have persisted it — not supplied by the test at retry time.
	eventID, err := st.InsertAlertEvent(ctx, &store.AlertEvent{
		RuleName: "r", ContainerName: "web-1", Message: "boom", HostID: 1, Severity: "critical", Project: "shop-prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMaintenanceWindow(ctx, &store.MaintenanceWindow{
		Name: "deploy grace", Reason: "auto", Project: "shop-prod",
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
		t.Fatalf("a project-scoped window should suppress the retry without spending an attempt: %+v err=%v", rows, err)
	}
	deliveries, err := st.AlertDeliveriesFor(ctx, []int64{eventID})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries[eventID]) != 0 {
		t.Fatalf("a skipped-for-suppression retry must not record a delivery attempt: %+v", deliveries[eventID])
	}
}

// TestSweepDoesNotHangOnAnUnresponsiveSMTPServer is the fix for a real bug:
// attemptEmail's SMTP send had no deadline on the connection itself (only
// net/smtp.SendMail's opaque net.Dial, no timeout at all), and the retry
// sweep runs one batch sequentially in a single goroutine — a server that
// accepts the TCP connection and then never greets would hang the whole
// sweep forever, silently stalling every OTHER queued retry (including
// unrelated webhooks) behind it. This proves SendMail itself returns once
// its context expires, well under a test timeout, against exactly that kind
// of server.
func TestSweepDoesNotHangOnAnUnresponsiveSMTPServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		// Accept and then say nothing at all — never sends the SMTP greeting.
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn // deliberately never written to or closed by us
		}
	}()
	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err = SendMail(ctx, store.SMTPConfig{
		Host: host, Port: port, From: "a@example.com", To: "b@example.com",
	}, "subject", "body")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a server that never greets should fail the send")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("SendMail should have returned once its 2s context expired, took %s", elapsed)
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
