package store

import (
	"context"
	"encoding/json"
)

const ldapSettingKey = "ldap_config"

// LDAPConfig configures optional LDAP / Active Directory authentication. The
// bind password is encrypted at rest (like the SMTP one) and never returned.
type LDAPConfig struct {
	Enabled      bool   `json:"enabled"`
	URL          string `json:"url"`      // ldap://host:389 or ldaps://host:636
	StartTLS     bool   `json:"startTls"` // upgrade a plain connection to TLS
	BindDN       string `json:"bindDn"`   // service account used to search for users
	BindPassword string `json:"bindPassword"`
	UserBaseDN   string `json:"userBaseDn"`
	UserFilter   string `json:"userFilter"`   // e.g. (uid=%s) or (sAMAccountName=%s)
	AdminGroupDN string `json:"adminGroupDn"` // optional: members are provisioned as admins
}

// Configured reports whether enough is set to attempt LDAP authentication.
func (c LDAPConfig) Configured() bool {
	return c.Enabled && c.URL != "" && c.UserBaseDN != "" && c.UserFilter != ""
}

// GetLDAP loads the LDAP config, decrypting the bind password.
func (s *Store) GetLDAP(ctx context.Context) (LDAPConfig, error) {
	raw, err := s.Setting(ctx, ldapSettingKey)
	if err != nil || raw == "" {
		return LDAPConfig{}, err
	}
	var c LDAPConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return LDAPConfig{}, err
	}
	if c.BindPassword != "" && s.cipher != nil {
		if pw, err := s.cipher.Decrypt(c.BindPassword); err == nil {
			c.BindPassword = pw
		}
	}
	return c, nil
}

// SetLDAP persists the config, encrypting the bind password. An empty bind
// password preserves the previously stored one.
func (s *Store) SetLDAP(ctx context.Context, c LDAPConfig) error {
	if c.BindPassword == "" {
		if prev, err := s.GetLDAP(ctx); err == nil {
			c.BindPassword = prev.BindPassword
		}
	}
	if c.BindPassword != "" && s.cipher != nil {
		enc, err := s.cipher.Encrypt(c.BindPassword)
		if err != nil {
			return err
		}
		c.BindPassword = enc
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, ldapSettingKey, string(b))
}
