package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/smtp"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// emailNotify sends a fired alert to the configured SMTP recipient. It runs in
// its own goroutine so a slow mail server never blocks the engine.
func (m *Monitor) emailNotify(ev *store.AlertEvent) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cfg, err := m.store.GetSMTP(ctx)
		if err != nil || cfg.Host == "" || cfg.From == "" {
			if err != nil {
				log.Printf("monitor: smtp config: %v", err)
			}
			return
		}
		// Per-host recipient override: a host may route its alerts elsewhere.
		if ev.HostID != 0 {
			if h, err := m.store.HostByID(ctx, ev.HostID); err == nil && h.AlertEmail != "" {
				cfg.To = h.AlertEmail
			}
		}
		if cfg.To == "" {
			return // no global or per-host recipient
		}
		subject := fmt.Sprintf("[%s] %s — %s", strings.ToUpper(ev.Severity), ev.RuleName, ev.ContainerName)
		body := fmt.Sprintf("Rule: %s\nType: %s\nSeverity: %s\nContainer: %s (%s)\nMessage: %s\nTime: %s\n",
			ev.RuleName, ev.Type, ev.Severity, ev.ContainerName, shortID(ev.ContainerID), ev.Message,
			time.Now().UTC().Format(time.RFC3339))
		if err := SendMail(cfg, subject, body); err != nil {
			log.Printf("monitor: email send failed: %v", err)
		}
	}()
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
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
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

func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
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
