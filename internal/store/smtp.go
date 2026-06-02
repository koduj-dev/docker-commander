package store

import (
	"context"
	"encoding/json"
)

const smtpSettingKey = "smtp_config"

// SMTPConfig is the mail server used for the email alert channel. The password
// is stored encrypted at rest (the persisted JSON holds ciphertext); it is
// decrypted on read and never returned to API clients.
type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"`  // comma-separated recipients
	TLS      bool   `json:"tls"` // implicit TLS (e.g. port 465); otherwise STARTTLS if offered
}

// Configured reports whether enough is set to attempt sending.
func (c SMTPConfig) Configured() bool { return c.Host != "" && c.From != "" && c.To != "" }

// GetSMTP loads the SMTP config, decrypting the password.
func (s *Store) GetSMTP(ctx context.Context) (SMTPConfig, error) {
	raw, err := s.Setting(ctx, smtpSettingKey)
	if err != nil || raw == "" {
		return SMTPConfig{}, err
	}
	var c SMTPConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return SMTPConfig{}, err
	}
	if c.Password != "" && s.cipher != nil {
		if pw, err := s.cipher.Decrypt(c.Password); err == nil {
			c.Password = pw
		}
	}
	return c, nil
}

// SetSMTP persists the SMTP config, encrypting the password. An empty password
// preserves the previously stored one (so the UI need not resend the secret).
func (s *Store) SetSMTP(ctx context.Context, c SMTPConfig) error {
	if c.Password == "" {
		if prev, err := s.GetSMTP(ctx); err == nil {
			c.Password = prev.Password
		}
	}
	if c.Password != "" && s.cipher != nil {
		enc, err := s.cipher.Encrypt(c.Password)
		if err != nil {
			return err
		}
		c.Password = enc
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, smtpSettingKey, string(b))
}
