package store

import (
	"context"
	"testing"
	"time"
)

func deliveryRetryStore(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eventID, err := st.InsertAlertEvent(context.Background(), &AlertEvent{
		RuleName: "cpu", Severity: "critical", HostID: 1, ContainerName: "web-1", Message: "boom",
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, eventID
}

func TestDueAlertDeliveryRetriesOnlyReturnsDueRows(t *testing.T) {
	st, eventID := deliveryRetryStore(t)
	ctx := context.Background()
	whID := int64(7)

	if err := st.EnqueueAlertDeliveryRetry(ctx, &AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &whID,
		NextAttemptAt: time.Now().Add(-time.Minute), LastError: "connection refused",
	}); err != nil {
		t.Fatal(err)
	}
	notYetID := eventID // reuse; a second row on the same event is fine
	if err := st.EnqueueAlertDeliveryRetry(ctx, &AlertDeliveryRetry{
		EventID: notYetID, Channel: "webhook", WebhookID: &whID,
		NextAttemptAt: time.Now().Add(time.Hour), LastError: "timeout",
	}); err != nil {
		t.Fatal(err)
	}

	due, err := st.DueAlertDeliveryRetries(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("expected exactly the one due retry, got %d", len(due))
	}
	if due[0].LastError != "connection refused" {
		t.Errorf("wrong row returned: %+v", due[0])
	}
	if due[0].WebhookID == nil || *due[0].WebhookID != whID {
		t.Errorf("webhook id did not round-trip: %+v", due[0])
	}
}

func TestDueAlertDeliveryRetriesRoundTripsEmailRecipients(t *testing.T) {
	st, eventID := deliveryRetryStore(t)
	ctx := context.Background()

	if err := st.EnqueueAlertDeliveryRetry(ctx, &AlertDeliveryRetry{
		EventID: eventID, Channel: "email", RuleEmails: []string{"ops@example.com", "oncall@example.com"},
		NextAttemptAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	due, err := st.DueAlertDeliveryRetries(ctx, time.Now(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("expected one due retry, got %d err=%v", len(due), err)
	}
	if due[0].WebhookID != nil {
		t.Errorf("an email retry should have no webhook id, got %v", due[0].WebhookID)
	}
	if len(due[0].RuleEmails) != 2 || due[0].RuleEmails[0] != "ops@example.com" {
		t.Errorf("recipients did not round-trip: %+v", due[0].RuleEmails)
	}
}

func TestRescheduleAlertDeliveryRetryIncrementsAttemptWhenAsked(t *testing.T) {
	st, eventID := deliveryRetryStore(t)
	ctx := context.Background()
	whID := int64(1)

	if err := st.EnqueueAlertDeliveryRetry(ctx, &AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &whID, NextAttemptAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.DueAlertDeliveryRetries(ctx, time.Now(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("setup: %d rows err=%v", len(rows), err)
	}
	id := rows[0].ID

	if err := st.RescheduleAlertDeliveryRetry(ctx, id, true, time.Now().Add(time.Hour), "still 500"); err != nil {
		t.Fatal(err)
	}
	// No longer due (pushed an hour out) and its attempt count advanced.
	due, err := st.DueAlertDeliveryRetries(ctx, time.Now(), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("rescheduled retry should not be due yet, got %d", len(due))
	}
	future, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(2*time.Hour), 10)
	if err != nil || len(future) != 1 || future[0].Attempt != 1 {
		t.Fatalf("expected attempt=1 after one increment-reschedule, got %+v err=%v", future, err)
	}
}

// TestRescheduleAlertDeliveryRetryCanSkipTheAttemptIncrement is the
// maintenance-window interaction: if a window is now covering the event, a
// retry sweep skips the actual send but must NOT burn down the attempt
// budget for a delivery it deliberately chose not to try.
func TestRescheduleAlertDeliveryRetryCanSkipTheAttemptIncrement(t *testing.T) {
	st, eventID := deliveryRetryStore(t)
	ctx := context.Background()
	whID := int64(1)

	if err := st.EnqueueAlertDeliveryRetry(ctx, &AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &whID, NextAttemptAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.DueAlertDeliveryRetries(ctx, time.Now(), 10)
	id := rows[0].ID

	if err := st.RescheduleAlertDeliveryRetry(ctx, id, false, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	future, err := st.DueAlertDeliveryRetries(ctx, time.Now().Add(2*time.Hour), 10)
	if err != nil || len(future) != 1 || future[0].Attempt != 0 {
		t.Fatalf("a suppressed skip must not increment the attempt count: %+v err=%v", future, err)
	}
}

func TestDeleteAlertDeliveryRetryRemovesIt(t *testing.T) {
	st, eventID := deliveryRetryStore(t)
	ctx := context.Background()
	whID := int64(1)

	if err := st.EnqueueAlertDeliveryRetry(ctx, &AlertDeliveryRetry{
		EventID: eventID, Channel: "webhook", WebhookID: &whID, NextAttemptAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.DueAlertDeliveryRetries(ctx, time.Now(), 10)
	if len(rows) != 1 {
		t.Fatalf("setup: %d rows", len(rows))
	}

	if err := st.DeleteAlertDeliveryRetry(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	after, err := st.DueAlertDeliveryRetries(ctx, time.Now(), 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("deleted retry should be gone, got %d", len(after))
	}
}

func TestAlertEventByIDRoundTripsSuppressedFields(t *testing.T) {
	st, eventID := deliveryRetryStore(t)
	ctx := context.Background()

	ev, err := st.AlertEventByID(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.RuleName != "cpu" || ev.Message != "boom" {
		t.Fatalf("event did not round-trip: %+v", ev)
	}

	if _, err := st.AlertEventByID(ctx, eventID+999); err != ErrNotFound {
		t.Fatalf("unknown event id: want ErrNotFound, got %v", err)
	}
}
