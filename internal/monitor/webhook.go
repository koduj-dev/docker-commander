package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"text/template"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// dispatcher sends fired alerts to configured webhooks.
type dispatcher struct {
	store  *store.Store
	client *http.Client
}

func newDispatcher(st *store.Store) *dispatcher {
	return &dispatcher{store: st, client: &http.Client{Timeout: 10 * time.Second}}
}

// payload is the data made available to a webhook's body template, and the
// default JSON body when no template is configured.
type payload struct {
	RuleName    string   `json:"ruleName"`
	Type        string   `json:"type"`
	Severity    string   `json:"severity"`
	Container   string   `json:"container"`
	ContainerID string   `json:"containerId"`
	Message     string   `json:"message"`
	Value       *float64 `json:"value,omitempty"`
	Time        string   `json:"time"`
}

// dispatch loads the webhook and POSTs the rendered payload. Runs in its own
// goroutine so a slow endpoint never blocks the engine.
// record stores the outcome so "we notified you" can be checked rather than
// assumed. Best-effort: a delivery that happened must not be lost because the
// bookkeeping failed, and a delivery that failed is already the bad news.
func (d *dispatcher) record(ctx context.Context, eventID int64, target string, ok bool, status int, detail string) {
	if eventID == 0 {
		return // the event itself failed to store; nothing to attach to
	}
	if err := d.store.RecordAlertDelivery(ctx, &store.AlertDelivery{
		EventID: eventID, Channel: "webhook", Target: target, OK: ok, Status: status, Detail: detail,
	}); err != nil {
		log.Printf("monitor: record webhook delivery: %v", err)
	}
}

func (d *dispatcher) dispatch(webhookID int64, ev *store.AlertEvent) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()

		wh, err := d.store.WebhookByID(ctx, webhookID)
		if err != nil {
			log.Printf("monitor: webhook %d not found: %v", webhookID, err)
			d.record(ctx, ev.ID, sprintf("webhook #%d", webhookID), false, 0, "webhook not found: "+err.Error())
			return
		}
		// Name plus host only. A webhook URL routinely carries a token in its
		// path or query, and this lands in a table any alerts reader can see.
		target := wh.Name + " (" + hostOf(wh.URL) + ")"

		p := payload{
			RuleName: ev.RuleName, Type: ev.Type, Severity: ev.Severity,
			Container: ev.ContainerName, ContainerID: ev.ContainerID,
			Message: ev.Message, Value: ev.Value,
			Time: time.Now().UTC().Format(time.RFC3339),
		}

		body, contentType := renderBody(wh.BodyTemplate, p)
		method := wh.Method
		if method == "" {
			method = http.MethodPost
		}
		req, err := http.NewRequestWithContext(ctx, method, wh.URL, bytes.NewReader(body))
		if err != nil {
			log.Printf("monitor: webhook request build: %v", err)
			d.record(ctx, ev.ID, target, false, 0, "bad request: "+err.Error())
			return
		}
		req.Header.Set("Content-Type", contentType)
		for k, v := range wh.Headers {
			req.Header.Set(k, v)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			log.Printf("monitor: webhook %q POST failed: %v", wh.Name, err)
			d.record(ctx, ev.ID, target, false, 0, err.Error())
			return
		}
		defer resp.Body.Close()
		// Keep a short excerpt: the endpoint's own words are usually what tells
		// an operator why it refused. Capped so a chatty endpoint can't write a
		// megabyte into our database.
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		io.Copy(io.Discard, resp.Body)
		ok := resp.StatusCode < 300
		if !ok {
			log.Printf("monitor: webhook %q returned %d", wh.Name, resp.StatusCode)
		}
		d.record(ctx, ev.ID, target, ok, resp.StatusCode, string(excerpt))
	}()
}

// renderBody produces the request body. With no template, it sends the payload
// as JSON; with a template, it renders it (text/plain unless it parses as JSON).
func renderBody(tmpl string, p payload) ([]byte, string) {
	if tmpl == "" {
		b, _ := json.Marshal(p)
		return b, "application/json"
	}
	t, err := template.New("wh").Parse(tmpl)
	if err != nil {
		b, _ := json.Marshal(p)
		return b, "application/json"
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		b, _ := json.Marshal(p)
		return b, "application/json"
	}
	out := buf.Bytes()
	if json.Valid(out) {
		return out, "application/json"
	}
	return out, "text/plain"
}

// hostOf returns just the host of a URL, for showing which endpoint was called
// without echoing any credential the URL might carry.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "unknown host"
	}
	return u.Host
}
