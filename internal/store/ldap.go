package store

import (
	"context"
	"encoding/json"
	"strings"
)

const ldapSettingKey = "ldap_config"

// LDAPGroupMapping grants a set of RBAC sections to members of an LDAP group,
// matched on the group's full DN. A user's effective sections are the union over
// every mapping whose group they belong to.
type LDAPGroupMapping struct {
	GroupDN  string   `json:"groupDn"`
	Sections []string `json:"sections"`
}

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
	// GroupMappings grant RBAC sections by LDAP group membership. When any are
	// set, LDAP is authoritative for a user's sections (re-synced on each login).
	GroupMappings []LDAPGroupMapping `json:"groupMappings"`
}

// cleanGroupMappings drops blank group DNs and any unknown section keys so a
// mapping can only ever grant real sections (no escalation via a bogus name).
func cleanGroupMappings(in []LDAPGroupMapping) []LDAPGroupMapping {
	out := make([]LDAPGroupMapping, 0, len(in))
	for _, m := range in {
		dn := strings.TrimSpace(m.GroupDN)
		if dn == "" {
			continue
		}
		seen := map[string]bool{}
		secs := make([]string, 0, len(m.Sections))
		for _, s := range m.Sections {
			if ValidSection(s) && !seen[s] {
				seen[s] = true
				secs = append(secs, s)
			}
		}
		out = append(out, LDAPGroupMapping{GroupDN: dn, Sections: secs})
	}
	return out
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
	c.GroupMappings = cleanGroupMappings(c.GroupMappings)
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
