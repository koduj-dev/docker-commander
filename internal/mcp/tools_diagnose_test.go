package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestListAlertRulesWithholdsDeliveryDetail.
//
// A rule carries where its alerts are sent: e-mail recipients, and a webhook
// whose URL routinely contains a token. None of that helps a model interpret an
// alert — knowing the THRESHOLD does — so the tool reports which channels a rule
// uses and nothing about where they point. This is the kind of field that gets
// added later "for completeness" by someone who hasn't thought about what a
// webhook URL contains.
func TestListAlertRulesWithholdsDeliveryDetail(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}

	whID, err := h.deps.Store.CreateWebhook(ctx, &store.Webhook{
		Name: "ops", URL: "https://hooks.example.com/services/T0/B0/SuperSecretToken",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.deps.Store.CreateAlertRule(ctx, &store.AlertRule{
		Name: "Memory", Enabled: true, Type: "resource", Severity: "warning",
		Config:    `{"metric":"mem","op":">","threshold":5,"durationSec":30}`,
		WebhookID: &whID, Email: true, Emails: []string{"oncall@example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	_, out, err := h.listAlertRules(ctx, reqFor(&principal{user: u}), listAlertRulesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(out.Rules))
	}
	r := out.Rules[0]

	// The threshold must be there — without it the alert message cannot be judged.
	if !strings.Contains(r.Config, "threshold") {
		t.Errorf("the rule's threshold is the whole point of this tool: %+v", r)
	}
	// The delivery detail must not be.
	blob := r.Name + r.Config + r.Notifies + r.Target
	for _, secret := range []string{"SuperSecretToken", "hooks.example.com", "oncall@example.com"} {
		if strings.Contains(blob, secret) {
			t.Errorf("SECURITY: %q reached the MCP client via list_alert_rules: %+v", secret, r)
		}
	}
	// Which channels it uses is fine, and useful.
	if !strings.Contains(r.Notifies, "webhook") || !strings.Contains(r.Notifies, "email") {
		t.Errorf("notifies should name the channels in use, got %q", r.Notifies)
	}
}

func TestSearchLogsRejectsUnusablePatterns(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	ctx := context.Background()
	u, err := h.deps.Store.UserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	req := reqFor(&principal{user: u})

	// An empty pattern would match every line of every container — an accidental
	// full log dump rather than a search.
	if _, _, err := h.searchLogs(ctx, req, searchLogsInput{Pattern: "   "}); err == nil {
		t.Error("an empty pattern must be refused, not treated as 'match everything'")
	}
	if _, _, err := h.searchLogs(ctx, req, searchLogsInput{Pattern: strings.Repeat("x", searchMaxPattern+1)}); err == nil {
		t.Error("an oversized pattern must be refused")
	}
	// A malformed regex is the caller's error and must come back as one rather
	// than panicking the server.
	if _, _, err := h.searchLogs(ctx, req, searchLogsInput{Pattern: "(unclosed", Regex: true}); err == nil {
		t.Error("an invalid regular expression must be reported")
	}
}
