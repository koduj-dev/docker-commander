package monitor

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestRenderBody(t *testing.T) {
	p := payload{RuleName: "r", Severity: "critical", Container: "web", Message: "boom"}

	// No template → JSON.
	body, ct := renderBody("", p)
	if ct != "application/json" || len(body) == 0 {
		t.Errorf("default body: %q %s", body, ct)
	}
	// JSON template → application/json.
	body, ct = renderBody(`{"text":"{{.Severity}} {{.Container}}"}`, p)
	if ct != "application/json" || string(body) != `{"text":"critical web"}` {
		t.Errorf("json template: %q %s", body, ct)
	}
	// Plain-text template → text/plain.
	_, ct = renderBody(`{{.RuleName}}: {{.Message}}`, p)
	if ct != "text/plain" {
		t.Errorf("text template content-type: %s", ct)
	}
	// Invalid template falls back to JSON (never panics).
	if _, ct := renderBody(`{{.Nope`, p); ct != "application/json" {
		t.Errorf("broken template should fall back to JSON, got %s", ct)
	}
}

func TestDispatchPostsToWebhook(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := crypto.New(key)
	st.SetCipher(c)
	ctx := context.Background()

	received := make(chan string, 1)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
	}))
	defer recv.Close()

	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL, BodyTemplate: `{{.Container}}|{{.Message}}`})
	if err != nil {
		t.Fatal(err)
	}
	d := newDispatcher(st)
	d.dispatch(whID, &store.AlertEvent{RuleName: "r", ContainerName: "web", Message: "boom"})

	select {
	case body := <-received:
		if body != "web|boom" {
			t.Errorf("webhook body = %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Error("webhook was not called")
	}
}

// TestDispatch_RecordSurvivesSendContextExpiring is the regression test for
// dispatch() sharing one context across the send and its bookkeeping. The
// test endpoint sleeps past deliveryRetryWebhookTimeout (attempt()'s own send
// budget), with the client's own http.Client.Timeout set even longer so the
// SEND's context — not the client's timeout — is what cuts the request off.
// Under the old shared-context code, record() would reuse that same
// already-expired context and RecordAlertDelivery would fail immediately
// against it, so no delivery row would ever appear. After the fix, record()
// gets its own fresh context and the row appears despite the send having
// timed out.
func TestDispatch_RecordSurvivesSendContextExpiring(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps past the send timeout; skipped under -short")
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := crypto.New(key)
	st.SetCipher(c)
	ctx := context.Background()

	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(deliveryRetryWebhookTimeout + 2*time.Second)
	}))
	defer recv.Close()

	whID, err := st.CreateWebhook(ctx, &store.Webhook{Name: "wh", URL: recv.URL})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.InsertAlertEvent(ctx, &store.AlertEvent{RuleName: "r", ContainerName: "web", Message: "boom"})
	if err != nil {
		t.Fatal(err)
	}

	// Bypass newDispatcher's 10s client.Timeout — it must be longer than
	// deliveryRetryWebhookTimeout so the SEND CONTEXT is what cuts the
	// request off, not the http.Client's own timeout.
	d := &dispatcher{store: st, client: &http.Client{Timeout: deliveryRetryWebhookTimeout + 10*time.Second}}
	d.dispatch(whID, &store.AlertEvent{ID: eventID, RuleName: "r", ContainerName: "web", Message: "boom"})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		deliveries, err := st.AlertDeliveriesFor(ctx, []int64{eventID})
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries[eventID]) > 0 {
			return // record() wrote the outcome despite the send's own context having expired
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no delivery was recorded within the deadline — record() likely reused the send's already-expired context")
}

// TestRedactURLKeepsSecretsOutOfDeliveryRecords.
//
// net/http wraps transport failures in *url.Error, whose message embeds the full
// request URL — and a webhook URL routinely carries a token in its path or query
// (Slack's /services/T00/B00/xxxx is the everyday example). The delivery record
// is readable by anyone holding the alerts section, so storing the raw error
// would leak that token to every such reader, while the target field next to it
// was carefully limited to a name and a host.
func TestRedactURLKeepsSecretsOutOfDeliveryRecords(t *testing.T) {
	const secret = "T00000/B11111/SuperSecretToken"
	wrapped := &url.Error{
		Op:  "Post",
		URL: "https://hooks.example.com/services/" + secret,
		Err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused"),
	}

	got := redactURL(wrapped)
	if strings.Contains(got, secret) {
		t.Fatalf("SECURITY: the webhook URL (and its token) reached the delivery record: %q", got)
	}
	if strings.Contains(got, "hooks.example.com") {
		t.Errorf("the URL should not be in the stored detail at all: %q", got)
	}
	// It still has to be useful — a redaction that throws away the cause turns a
	// diagnosable failure into "it didn't work".
	if !strings.Contains(got, "connection refused") {
		t.Errorf("the cause was lost, leaving nothing to diagnose: %q", got)
	}

	// A plain error passes through unchanged.
	if got := redactURL(errors.New("boom")); got != "boom" {
		t.Errorf("non-URL error = %q, want %q", got, "boom")
	}
}

// PENTEST: the subject carries an alert rule's name, chosen by any user holding
// the alerts section. CR/LF in that name ends the Subject header and starts
// another one — a Reply-To pointing elsewhere, or a Content-Type that turns the
// alert into HTML the recipient's client renders.
//
// The envelope is not injectable (net/smtp rejects CR/LF in MAIL/RCPT), so this
// is header and content forgery rather than silent redirection. Quite enough.
func TestPentestMailHeaderInjectionIsNeutralised(t *testing.T) {
	msg := string(buildMessage(
		"alerts@example.com",
		"ops@example.com",
		"CPU high\r\nReply-To: attacker@evil.test\r\nContent-Type: text/html",
		"body text",
	))

	head, body, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok {
		t.Fatal("message has no header/body separator")
	}
	// Asserted per LINE, not by substring: the flattened text still mentions
	// "Reply-To:" inside the subject, which is harmless — what must not exist is
	// a header line beginning with it. Searching the whole block would fail on
	// correct output and pass on nothing useful.
	var replyTo, contentType int
	for _, line := range strings.Split(head, "\r\n") {
		switch {
		case strings.HasPrefix(strings.ToLower(line), "reply-to:"):
			replyTo++
		case strings.HasPrefix(strings.ToLower(line), "content-type:"):
			contentType++
		}
	}
	if replyTo != 0 {
		t.Errorf("SECURITY: an injected Reply-To header line survived:\n%s", head)
	}
	if contentType != 1 {
		t.Errorf("SECURITY: %d Content-Type header lines, want 1:\n%s", contentType, head)
	}
	if body != "body text" {
		t.Errorf("body should be untouched, got %q", body)
	}
	// The rule name still appears, just flattened — the operator must be able to
	// tell which rule fired.
	if !strings.Contains(head, "CPU high") {
		t.Errorf("the rule name should survive as text:\n%s", head)
	}
}

// A normal subject must be passed through exactly, otherwise every alert mail
// pays for the guard.
func TestMailSubjectPassesThroughUnchanged(t *testing.T) {
	msg := string(buildMessage("a@example.com", "b@example.com", "MEM critical > 90%", "x"))
	if !strings.Contains(msg, "Subject: MEM critical > 90%\r\n") {
		t.Errorf("subject was altered:\n%s", msg)
	}
}
