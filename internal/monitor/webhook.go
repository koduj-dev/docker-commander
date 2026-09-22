package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// dispatch's send and the two bookkeeping calls after it each get their OWN
// context (deliveryRetryWebhookTimeout for the send, deliveryRetryDBTimeout
// for each store call) — mirroring retryOne in delivery_retry.go, and for the
// same reason: sharing one budget across all three meant a slow-but-not-hung
// endpoint could consume most of it in attempt()'s own client.Timeout,
// leaving record()/enqueueRetry() racing real DB latency against an
// almost-expired context and silently losing the delivery record or the
// retry enqueue.
func (d *dispatcher) dispatch(webhookID int64, ev *store.AlertEvent) {
	go func() {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), deliveryRetryWebhookTimeout)
		ok, status, target, detail, retriable := d.attempt(sendCtx, webhookID, ev)
		sendCancel()

		recordCtx, recordCancel := context.WithTimeout(context.Background(), deliveryRetryDBTimeout)
		d.record(recordCtx, ev.ID, target, ok, status, detail)
		recordCancel()

		if !ok && retriable {
			enqueueCtx, enqueueCancel := context.WithTimeout(context.Background(), deliveryRetryDBTimeout)
			d.enqueueRetry(enqueueCtx, ev.ID, webhookID, detail)
			enqueueCancel()
		}
	}()
}

// attempt performs ONE webhook POST, synchronously. Split out from dispatch
// so the retry sweep (delivery_retry.go) can drive the exact same attempt —
// build the request, read the response, decide what happened — without
// re-implementing it or spawning its own goroutine (the sweep already runs
// off its own loop).
//
// retriable distinguishes a failure worth trying again (no response at all,
// or the endpoint itself said "try later": 429 or 5xx) from one that won't
// fix itself (a bad webhook id, a malformed request, or a 4xx the endpoint
// used to reject the payload/auth outright) — reattempting the latter
// indefinitely would just hammer a misconfigured endpoint.
func (d *dispatcher) attempt(ctx context.Context, webhookID int64, ev *store.AlertEvent) (ok bool, status int, target, detail string, retriable bool) {
	wh, err := d.store.WebhookByID(ctx, webhookID)
	if err != nil {
		log.Printf("monitor: webhook %d not found: %v", webhookID, err)
		return false, 0, sprintf("webhook #%d", webhookID), "webhook not found: " + err.Error(), false
	}
	// Name plus host only. A webhook URL routinely carries a token in its
	// path or query, and this lands in a table any alerts reader can see.
	target = wh.Name + " (" + hostOf(wh.URL) + ")"

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
		return false, 0, target, "bad request: " + redactURL(err), false
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range wh.Headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		log.Printf("monitor: webhook %q POST failed: %v", wh.Name, err)
		return false, 0, target, redactURL(err), true
	}
	defer resp.Body.Close()
	// Keep a short excerpt: the endpoint's own words are usually what tells
	// an operator why it refused. Capped so a chatty endpoint can't write a
	// megabyte into our database.
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	io.Copy(io.Discard, resp.Body)
	ok = resp.StatusCode < 300
	if !ok {
		log.Printf("monitor: webhook %q returned %d", wh.Name, resp.StatusCode)
	}
	retriable = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return ok, resp.StatusCode, target, string(excerpt), retriable
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

// redactURL returns a transport error's cause WITHOUT the URL it was reaching.
//
// net/http wraps failures in *url.Error, whose message embeds the full request
// URL — and webhook URLs routinely carry a token in the path or query. Storing
// err.Error() would therefore write that secret into alert_deliveries, which is
// readable by anyone holding the alerts section, undoing the care taken to keep
// the target field to a name and a host. The underlying cause ("dial tcp:
// connection refused", "context deadline exceeded") is the diagnostic part, and
// it carries no URL.
func redactURL(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}
