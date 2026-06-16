package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func newAlertServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{cfg: config.Config{DataDir: t.TempDir()}, store: st}
}

// exportBundle calls the export handler and decodes the bundle.
func exportBundle(t *testing.T, srv *Server) alertRuleBundle {
	t.Helper()
	w := httptest.NewRecorder()
	srv.handleExportAlertRules(w, httptest.NewRequest("GET", "/api/alert-rules/export", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d, body %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("export missing attachment disposition: %q", cd)
	}
	var b alertRuleBundle
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	return b
}

// importBundle posts a raw JSON body to the import handler.
func importBundle(t *testing.T, srv *Server, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/alert-rules/import", bytes.NewBufferString(body))
	srv.handleImportAlertRules(w, r)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func TestAlertRulesExportImportRoundTrip(t *testing.T) {
	srv := newAlertServer(t)
	ctx := context.Background()
	hookID, err := srv.store.CreateWebhook(ctx, &store.Webhook{Name: "slack", URL: "https://hooks.example/x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateAlertRule(ctx, &store.AlertRule{
		Name: "die-alert", Enabled: true, Type: "state", Target: "web",
		Config: `{"events":["die"]}`, Severity: "critical", WebhookID: &hookID,
		Email: true, CooldownSec: 120,
	}); err != nil {
		t.Fatal(err)
	}

	bundle := exportBundle(t, srv)
	if bundle.Version != alertExportVersion || len(bundle.Rules) != 1 {
		t.Fatalf("unexpected bundle: %+v", bundle)
	}
	pr := bundle.Rules[0]
	// Webhook is exported by NAME, never by id; secrets (the URL) are absent.
	if pr.Webhook != "slack" {
		t.Errorf("webhook should export by name, got %q", pr.Webhook)
	}
	if strings.Contains(string(mustJSON(t, bundle)), "hooks.example") {
		t.Error("export must not leak the webhook URL/secret")
	}

	// Re-import into a fresh instance that also has a webhook named "slack".
	dst := newAlertServer(t)
	if _, err := dst.store.CreateWebhook(ctx, &store.Webhook{Name: "slack", URL: "https://other/y"}); err != nil {
		t.Fatal(err)
	}
	w, resp := importBundle(t, dst, string(mustJSON(t, bundle)))
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d (%s)", w.Code, w.Body.String())
	}
	if resp["imported"].(float64) != 1 {
		t.Errorf("expected 1 imported, got %v (warnings %v)", resp["imported"], resp["warnings"])
	}
	rules, _ := dst.store.ListAlertRules(ctx)
	if len(rules) != 1 || rules[0].Name != "die-alert" || rules[0].WebhookID == nil {
		t.Fatalf("imported rule not as expected: %+v", rules)
	}
	if rules[0].Severity != "critical" || rules[0].CooldownSec != 120 || !rules[0].Email {
		t.Errorf("imported rule fields drifted: %+v", rules[0])
	}
}

// PENTEST: a bundle must not be able to inject a rule with an arbitrary type or
// severity (which the alert engine would mis-handle), nor an over-size payload.
func TestImportRejectsMaliciousRules(t *testing.T) {
	srv := newAlertServer(t)
	cases := []struct {
		name string
		rule string
	}{
		{"bad type", `{"name":"x","type":"rootkit","config":{}}`},
		{"bad severity", `{"name":"x","type":"state","severity":"apocalyptic","config":{}}`},
		{"config not an object", `{"name":"x","type":"state","config":[1,2,3]}`},
		{"config scalar", `{"name":"x","type":"state","config":"pwned"}`},
		{"empty name", `{"name":"  ","type":"state","config":{}}`},
		{"oversize name", `{"name":"` + strings.Repeat("A", 300) + `","type":"state","config":{}}`},
		{"oversize config", `{"name":"x","type":"state","config":{"k":"` + strings.Repeat("A", 20000) + `"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"version":1,"rules":[` + c.rule + `]}`
			w, resp := importBundle(t, srv, body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := resp["imported"].(float64); got != 0 {
				t.Errorf("malicious rule was imported (%v)", got)
			}
		})
	}
	// Nothing should have been persisted.
	rules, _ := srv.store.ListAlertRules(context.Background())
	if len(rules) != 0 {
		t.Errorf("expected no rules persisted, got %d", len(rules))
	}
}

// PENTEST: an unknown webhook name must NOT silently attach the rule to some
// other (e.g. attacker-chosen) local webhook — it must be left unset + warned.
func TestImportUnknownWebhookLeftUnset(t *testing.T) {
	srv := newAlertServer(t)
	ctx := context.Background()
	// A decoy webhook exists, but the bundle names a different one.
	if _, err := srv.store.CreateWebhook(ctx, &store.Webhook{Name: "decoy", URL: "https://decoy/x"}); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"rules":[{"name":"x","type":"state","config":{},"webhook":"ghost"}]}`
	w, resp := importBundle(t, srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if resp["imported"].(float64) != 1 {
		t.Fatalf("rule should still import (webhook just unset): %v", resp)
	}
	if warns, _ := resp["warnings"].([]any); len(warns) == 0 {
		t.Error("expected a warning about the missing webhook")
	}
	rules, _ := srv.store.ListAlertRules(ctx)
	if len(rules) != 1 || rules[0].WebhookID != nil {
		t.Errorf("unknown webhook must leave the destination unset, got %+v", rules)
	}
}

// PENTEST: the rule count is bounded to prevent an import-bomb.
func TestImportRejectsTooManyRules(t *testing.T) {
	srv := newAlertServer(t)
	var sb strings.Builder
	sb.WriteString(`{"version":1,"rules":[`)
	for i := 0; i < maxImportRules+1; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"name":"r","type":"state","config":{}}`)
	}
	sb.WriteString(`]}`)
	w, _ := importBundle(t, srv, sb.String())
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an oversized bundle, got %d", w.Code)
	}
}

func TestImportRejectsBadVersion(t *testing.T) {
	srv := newAlertServer(t)
	w, _ := importBundle(t, srv, `{"version":99,"rules":[{"name":"x","type":"state","config":{}}]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown version, got %d", w.Code)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
