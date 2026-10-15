package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWebhookHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://hooks.slack.com/services/T0/B0/secret": "hooks.slack.com",
		"https://user:pass@ntfy.example:8443/topic?x=1": "ntfy.example:8443",
		"http://10.0.0.5/hook":                          "10.0.0.5",
		"hooks.example.com":                             "hooks.example.com", // already redacted
		"hooks.example.com/token":                       "",
		"/relative/token":                               "",
		"":                                              "",
	} {
		if got := WebhookHost(in); got != want {
			t.Errorf("WebhookHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// Audit rows written before webhook.create recorded only the host still hold
// the whole URL. Opening the store scrubs them to the host, leaves every other
// action alone, and is a no-op on the next start.
func TestPentestOldWebhookAuditIsRedactedOnOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dc.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(action, detail string) int64 {
		t.Helper()
		res, err := st.db.ExecContext(ctx, `INSERT INTO audit_log (action, target, detail, created_at) VALUES (?, 'ops', ?, '2026-01-01T00:00:00Z')`, action, detail)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	leaked := insert("webhook.create", "https://hooks.slack.com/services/T0/B0/s3cret?sig=s3cret")
	clean := insert("webhook.create", "hooks.example.com")
	other := insert("project.deploy", "https://git.example/repo/s3cret")
	st.Close()

	for round := 1; round <= 2; round++ {
		st, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		detail := func(id int64) string {
			var d string
			if err := st.db.QueryRowContext(ctx, `SELECT detail FROM audit_log WHERE id = ?`, id).Scan(&d); err != nil {
				t.Fatal(err)
			}
			return d
		}
		if got := detail(leaked); got != "hooks.slack.com" {
			t.Errorf("round %d: old webhook.create detail = %q, want the host", round, got)
		}
		if got := detail(clean); got != "hooks.example.com" {
			t.Errorf("round %d: already-clean detail changed to %q", round, got)
		}
		if got := detail(other); got != "https://git.example/repo/s3cret" {
			t.Errorf("round %d: another action's detail was touched: %q", round, got)
		}
		st.Close()
	}
}
