package store

import (
	"context"
	"encoding/json"
	"strings"
)

const ldapSettingKey = "ldap_config"

// LDAPGroupMapping grants access to members of an LDAP group, matched on the
// group's full DN. A mapping can hand out named roles, a raw list of sections,
// or both; a user's effective access is the union over every mapping whose group
// they belong to. Roles are the intended way to use this; the Sections field
// predates roles and stays for configs written before they existed.
type LDAPGroupMapping struct {
	GroupDN  string   `json:"groupDn"`
	Sections []string `json:"sections"`
	// omitempty so a config written before roles existed stays truly absent
	// rather than serialising as "roleIds": null.
	RoleIDs []int64 `json:"roleIds,omitempty"`
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
	// FallbackRoleID is granted in place of a mapped role that no longer exists,
	// so deleting a role degrades its members to a known baseline instead of
	// silently leaving them with nothing. 0 means no fallback. It applies only to
	// a mapping that matched and then failed to resolve — never to a user whose
	// groups map to nothing, which would hand access to every account in the
	// directory.
	FallbackRoleID int64 `json:"fallbackRoleId"`
}

// cleanGroupMappings drops blank group DNs, any unknown section keys and any
// non-positive role id, so a mapping can only ever grant real sections and
// plausible roles (no escalation via a bogus name). Role ids are not checked for
// existence here — a role deleted later would leave a stale id behind, so the id
// is re-checked when the mapping is applied at login.
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
		seenRole := map[int64]bool{}
		roles := make([]int64, 0, len(m.RoleIDs))
		for _, id := range m.RoleIDs {
			if id > 0 && !seenRole[id] {
				seenRole[id] = true
				roles = append(roles, id)
			}
		}
		out = append(out, LDAPGroupMapping{GroupDN: dn, Sections: secs, RoleIDs: roles})
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
	if c.FallbackRoleID < 0 {
		c.FallbackRoleID = 0
	}
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
