package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// emailNotify sends a fired alert by e-mail. It runs in its own goroutine so a
// slow mail server never blocks the engine.
//
// Recipients are resolved most-specific-first:
//
//  1. ruleEmails — the rule's own list, set by whoever wrote the rule.
//  2. the host's alert_email override, for a host that routes elsewhere.
//  3. the instance-wide SMTP "To".
//
// A rule created before per-rule recipients existed has an empty list, so it
// still resolves to 2 or 3 and delivers exactly as it did before.
func (m *Monitor) emailNotify(ev *store.AlertEvent, ruleEmails []string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cfg, err := m.store.GetSMTP(ctx)
		if err != nil || cfg.Host == "" || cfg.From == "" {
			if err != nil {
				log.Printf("monitor: smtp config: %v", err)
			}
			m.recordDelivery(ctx, ev.ID, "", false, "SMTP is not configured, so this alert was not e-mailed")
			return
		}
		if len(ruleEmails) > 0 {
			cfg.To = strings.Join(ruleEmails, ", ")
		} else if ev.HostID != 0 {
			// Per-host recipient override: a host may route its alerts elsewhere.
			if h, err := m.store.HostByID(ctx, ev.HostID); err == nil && h.AlertEmail != "" {
				cfg.To = h.AlertEmail
			}
		}
		if cfg.To == "" {
			m.recordDelivery(ctx, ev.ID, "", false, "no recipient: the rule, the host and the SMTP settings all leave it empty")
			return
		}
		subject := fmt.Sprintf("[%s] %s — %s", strings.ToUpper(ev.Severity), ev.RuleName, ev.ContainerName)
		body := fmt.Sprintf("Rule: %s\nType: %s\nSeverity: %s\nContainer: %s (%s)\nMessage: %s\nTime: %s\n",
			ev.RuleName, ev.Type, ev.Severity, ev.ContainerName, shortID(ev.ContainerID), ev.Message,
			time.Now().UTC().Format(time.RFC3339))
		if err := SendMail(cfg, subject, body); err != nil {
			log.Printf("monitor: email send failed: %v", err)
			m.recordDelivery(ctx, ev.ID, cfg.To, false, err.Error())
			return
		}
		m.recordDelivery(ctx, ev.ID, cfg.To, true, "")
	}()
}

// recordDelivery notes whether an alert actually left by e-mail.
//
// The failure cases matter more than the success one: an unconfigured SMTP
// server or an empty recipient list used to make emailNotify return silently, so
// a rule with "email" ticked looked like it was delivering when nothing was ever
// sent. Now that shows up against the alert.
func (m *Monitor) recordDelivery(ctx context.Context, eventID int64, to string, ok bool, detail string) {
	if eventID == 0 {
		return
	}
	target := to
	if target == "" {
		target = "(no recipient)"
	}
	if err := m.store.RecordAlertDelivery(ctx, &store.AlertDelivery{
		EventID: eventID, Channel: "email", Target: target, OK: ok, Detail: detail,
	}); err != nil {
		log.Printf("monitor: record email delivery: %v", err)
	}
}

// SendMail delivers one message via the configured SMTP server. It supports
// implicit TLS (cfg.TLS, e.g. port 465) and otherwise lets net/smtp negotiate
// STARTTLS when the server offers it. Exported so the API can send a test mail.
func SendMail(cfg store.SMTPConfig, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	recipients := splitRecipients(cfg.To)
	if len(recipients) == 0 {
		return fmt.Errorf("no recipients configured")
	}
	msg := buildMessage(cfg.From, cfg.To, subject, body)

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	if !cfg.TLS {
		// Plain connection; net/smtp upgrades to STARTTLS if the server offers it.
		return smtp.SendMail(addr, auth, cfg.From, recipients, msg)
	}

	// Implicit TLS: dial a TLS socket first, then speak SMTP over it.
	conn, err := tlsDial("tcp", addr, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	// NewClient takes ownership of conn even when it fails: it wraps the socket in
	// a textproto.Conn and closes that if the greeting doesn't arrive. So there is
	// nothing to close here — an explicit conn.Close() on this path would be dead
	// code, which is what it was until a mutation test showed it could not be made
	// to matter. TestSendMailClosesTheConnectionWhenTheGreetingFails pins the
	// contract we are relying on, so a refactor that stops handing the socket over
	// gets caught.
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// headerValue strips anything that would end the header and start another one.
//
// The subject carries an alert rule's name, which any user with the alerts
// section chooses. A name containing CR or LF injects arbitrary headers into the
// message: a Reply-To pointing somewhere else, or a Content-Type that turns the
// alert into HTML the recipient's client renders. The envelope is safe already —
// net/smtp rejects CR/LF in MAIL/RCPT arguments — so this is header and content
// forgery rather than silent redirection, which is quite enough.
//
// Replaced with a space rather than dropped, so a name that hits this still reads
// as itself in the subject line instead of running two words together.
func headerValue(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// tlsDial is tls.Dial, swappable so the ownership of the connection — who closes
// it when the SMTP handshake fails — can be asserted without a certificate
// authority. Production always uses tls.Dial.
var tlsDial = func(network, addr string, cfg *tls.Config) (net.Conn, error) {
	return tls.Dial(network, addr, cfg)
}

func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + headerValue(from) + "\r\n")
	b.WriteString("To: " + headerValue(to) + "\r\n")
	b.WriteString("Subject: " + headerValue(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

func splitRecipients(to string) []string {
	var out []string
	for _, r := range strings.Split(to, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
