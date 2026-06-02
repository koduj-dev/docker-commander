package store

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/crypto"
)

func newStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := crypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCipher(c)
	return s, context.Background()
}

func TestUsersCRUD(t *testing.T) {
	s, ctx := newStore(t)

	if n, _ := s.CountUsers(ctx); n != 0 {
		t.Fatalf("fresh store should have 0 users, got %d", n)
	}
	id, err := s.CreateUser(ctx, &User{Username: "admin", PasswordHash: "h", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByID(ctx, id)
	if err != nil || u.Username != "admin" || !u.IsAdmin() || u.AuthSource != "local" {
		t.Errorf("UserByID: %+v err=%v", u, err)
	}
	if _, err := s.UserByUsername(ctx, "admin"); err != nil {
		t.Errorf("UserByUsername: %v", err)
	}
	if _, err := s.UserByUsername(ctx, "ghost"); err != ErrNotFound {
		t.Errorf("missing user should be ErrNotFound, got %v", err)
	}

	// permissions + password + totp
	if err := s.UpdateUserAccess(ctx, id, "user", true, []string{"containers", "logs"}); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByID(ctx, id)
	if u.Role != "user" || !u.ReadOnly || len(u.Sections) != 2 {
		t.Errorf("UpdateUserAccess not applied: %+v", u)
	}
	if err := s.UpdatePassword(ctx, id, "newhash"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTOTP(ctx, id, "SECRET", true); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByID(ctx, id)
	if u.PasswordHash != "newhash" || !u.TOTPEnabled || u.TOTPSecret != "SECRET" {
		t.Errorf("password/totp not applied: %+v", u)
	}

	_, _ = s.CreateUser(ctx, &User{Username: "a2", Role: "admin"})
	if n, _ := s.CountAdmins(ctx); n != 1 {
		t.Errorf("expected 1 admin (id was demoted), got %d", n)
	}
	list, _ := s.ListUsers(ctx)
	if len(list) != 2 {
		t.Errorf("ListUsers expected 2, got %d", len(list))
	}
	if err := s.DeleteUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountUsers(ctx); n != 1 {
		t.Errorf("after delete expected 1 user, got %d", n)
	}
}

func TestHostsCRUD(t *testing.T) {
	s, ctx := newStore(t)
	if err := s.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalHost(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	hosts, _ := s.ListHosts(ctx)
	if len(hosts) != 1 || hosts[0].Kind != "local" {
		t.Fatalf("EnsureLocalHost: %+v", hosts)
	}

	id, err := s.CreateHost(ctx, &Host{Name: "prod", Kind: "ssh", Address: "deploy@prod"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostKey(ctx, id, "ssh-ed25519 AAAA..."); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostAlertEmail(ctx, id, "ops@x.io"); err != nil {
		t.Fatal(err)
	}
	h, _ := s.HostByID(ctx, id)
	if h.HostKey == "" || h.AlertEmail != "ops@x.io" {
		t.Errorf("host key/email not persisted: %+v", h)
	}
	if err := s.DeleteHost(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HostByID(ctx, id); err != ErrNotFound {
		t.Errorf("deleted host should be gone, got %v", err)
	}
}

func TestRegistriesEncrypted(t *testing.T) {
	s, ctx := newStore(t)
	id, err := s.CreateRegistry(ctx, "hub", "docker.io", "alice", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	// listing must not expose the secret
	regs, _ := s.ListRegistries(ctx)
	if len(regs) != 1 || regs[0].Username != "alice" {
		t.Fatalf("ListRegistries: %+v", regs)
	}
	// decrypts via AuthByID
	a, err := s.AuthByID(ctx, id)
	if err != nil || a.Password != "s3cret" || a.Username != "alice" {
		t.Errorf("AuthByID decrypt: %+v err=%v", a, err)
	}
	// matched by host (Docker Hub aliases normalise to docker.io)
	a2, err := s.AuthForHost(ctx, "index.docker.io")
	if err != nil || a2.Password != "s3cret" {
		t.Errorf("AuthForHost normalise: %+v err=%v", a2, err)
	}
	if _, err := s.AuthForHost(ctx, "ghcr.io"); err != ErrNotFound {
		t.Errorf("unknown host → ErrNotFound, got %v", err)
	}
	// the raw row must not contain the plaintext secret
	var enc string
	_ = s.db.QueryRowContext(ctx, `SELECT secret_enc FROM registries WHERE id=?`, id).Scan(&enc)
	if enc == "s3cret" || enc == "" {
		t.Errorf("secret should be stored encrypted, got %q", enc)
	}
	_ = s.DeleteRegistry(ctx, id)
	if regs, _ := s.ListRegistries(ctx); len(regs) != 0 {
		t.Error("registry not deleted")
	}
}

func TestNormalizeRegistryHost(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "docker.io",
		"index.docker.io":   "docker.io",
		"registry-1.docker.io": "docker.io",
		"GHCR.IO":           "ghcr.io",
		"https://quay.io/":  "quay.io",
		"localhost:5000":    "localhost:5000",
	} {
		if got := NormalizeRegistryHost(in); got != want {
			t.Errorf("NormalizeRegistryHost(%q)=%q want %q", in, got, want)
		}
	}
	if !ValidSection("containers") || ValidSection("nope") {
		t.Error("ValidSection wrong")
	}
}

func TestSMTPAndLDAPEncrypted(t *testing.T) {
	s, ctx := newStore(t)

	if err := s.SetSMTP(ctx, SMTPConfig{Host: "smtp.x", Port: 587, From: "a@x", To: "b@x", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.GetSMTP(ctx)
	if cfg.Password != "pw" || !cfg.Configured() {
		t.Errorf("smtp round trip: %+v", cfg)
	}
	// blank password preserves the stored one
	if err := s.SetSMTP(ctx, SMTPConfig{Host: "smtp.x", Port: 25, From: "a@x", To: "b@x"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.GetSMTP(ctx)
	if cfg.Password != "pw" || cfg.Port != 25 {
		t.Errorf("blank password should keep stored: %+v", cfg)
	}

	if err := s.SetLDAP(ctx, LDAPConfig{Enabled: true, URL: "ldap://x", UserBaseDN: "dc=x", UserFilter: "(uid=%s)", BindPassword: "bindpw"}); err != nil {
		t.Fatal(err)
	}
	l, _ := s.GetLDAP(ctx)
	if l.BindPassword != "bindpw" || !l.Configured() {
		t.Errorf("ldap round trip: %+v", l)
	}
}

func TestAccessSettings(t *testing.T) {
	s, ctx := newStore(t)
	if d, _ := s.DisabledSections(ctx); len(d) != 0 {
		t.Error("no sections disabled by default")
	}
	if err := s.SetDisabledSections(ctx, []string{"events", "topology", "bogus"}); err != nil {
		t.Fatal(err)
	}
	d, _ := s.DisabledSections(ctx)
	if len(d) != 2 { // "bogus" is dropped as invalid
		t.Errorf("invalid sections should be filtered: %v", d)
	}
	if on, _ := s.LocalhostNo2FA(ctx); on {
		t.Error("localhost-2FA off by default")
	}
	_ = s.SetLocalhostNo2FA(ctx, true)
	if on, _ := s.LocalhostNo2FA(ctx); !on {
		t.Error("localhost-2FA should be on")
	}
}

func TestAlertsCRUD(t *testing.T) {
	s, ctx := newStore(t)

	whID, err := s.CreateWebhook(ctx, &Webhook{Name: "slack", URL: "https://h"})
	if err != nil {
		t.Fatal(err)
	}
	if wh, err := s.WebhookByID(ctx, whID); err != nil || wh.Method != "POST" {
		t.Errorf("webhook default method: %+v err=%v", wh, err)
	}

	rid, err := s.CreateAlertRule(ctx, &AlertRule{Name: "cpu", Type: "resource", Enabled: true, WebhookID: &whID, Email: true})
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := s.ListAlertRules(ctx)
	if len(rules) != 1 || !rules[0].Enabled || rules[0].WebhookID == nil || !rules[0].Email {
		t.Errorf("rule not stored right: %+v", rules)
	}
	_ = s.SetAlertRuleEnabled(ctx, rid, false)
	if err := s.UpdateAlertRule(ctx, rid, &AlertRule{Name: "cpu2", Type: "resource", Severity: "critical", CooldownSec: 120}); err != nil {
		t.Fatal(err)
	}
	rules, _ = s.ListAlertRules(ctx)
	if rules[0].Name != "cpu2" || rules[0].Severity != "critical" || rules[0].CooldownSec != 120 {
		t.Errorf("UpdateAlertRule not applied: %+v", rules[0])
	}

	v := 95.0
	if _, err := s.InsertAlertEvent(ctx, &AlertEvent{RuleID: rid, RuleName: "cpu2", HostName: "local", ContainerName: "web", Message: "hot", Value: &v}); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.ListAlertEvents(ctx, 10)
	if len(evs) != 1 || evs[0].HostName != "local" || evs[0].Value == nil {
		t.Errorf("alert event: %+v", evs)
	}
	if n, _ := s.CountUnacknowledged(ctx); n != 1 {
		t.Errorf("expected 1 unacked, got %d", n)
	}
	_ = s.AckAlertEvent(ctx, evs[0].ID)
	if n, _ := s.CountUnacknowledged(ctx); n != 0 {
		t.Errorf("expected 0 unacked after ack, got %d", n)
	}
	_ = s.DeleteAlertRule(ctx, rid)
	_ = s.DeleteWebhook(ctx, whID)
}

func TestParseRulesAndSettingsAndAudit(t *testing.T) {
	s, ctx := newStore(t)

	id, err := s.CreateParseRule(ctx, "nginx", `(?<ip>\S+)`)
	if err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.ListParseRules(ctx); len(rs) != 1 || rs[0].Name != "nginx" {
		t.Errorf("parse rule: %+v", rs)
	}
	_ = s.DeleteParseRule(ctx, id)
	if rs, _ := s.ListParseRules(ctx); len(rs) != 0 {
		t.Error("parse rule not deleted")
	}

	if v, _ := s.Setting(ctx, "missing"); v != "" {
		t.Error("missing setting → empty")
	}
	_ = s.SetSetting(ctx, "k", "v1")
	_ = s.SetSetting(ctx, "k", "v2") // upsert
	if v, _ := s.Setting(ctx, "k"); v != "v2" {
		t.Errorf("setting upsert: %q", v)
	}

	if err := s.Audit(ctx, AuditEntry{Username: "admin", Action: "container.stop", Target: "web", IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.RecentAudit(ctx, 10)
	if len(entries) != 1 || entries[0].Action != "container.stop" {
		t.Errorf("audit: %+v", entries)
	}
}
