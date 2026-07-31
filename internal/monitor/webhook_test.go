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
