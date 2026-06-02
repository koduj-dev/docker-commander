package monitor

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
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
