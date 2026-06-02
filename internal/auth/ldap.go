package auth

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// LDAPTest verifies the LDAP settings: dial, optional StartTLS, service bind,
// and a base search. Returns the number of entries under the user base.
func LDAPTest(cfg store.LDAPConfig) (int, error) {
	conn, err := ldap.DialURL(cfg.URL)
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	if cfg.StartTLS {
		if err := conn.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12}); err != nil {
			return 0, fmt.Errorf("starttls: %w", err)
		}
	}
	if cfg.BindDN != "" {
		if err := conn.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			return 0, fmt.Errorf("service bind: %w", err)
		}
	}
	sr := ldap.NewSearchRequest(cfg.UserBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 50, 10, false,
		"(objectClass=*)", []string{"dn"}, nil)
	res, err := conn.Search(sr)
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}
	return len(res.Entries), nil
}

// LDAPResult is the outcome of a successful LDAP authentication.
type LDAPResult struct {
	Username string
	IsAdmin  bool // member of the configured admin group
}

// LDAPAuthenticate verifies a username/password against an LDAP/AD directory:
// bind with the service account, search for the user, then bind as that user to
// validate the password. If an admin group is configured, group membership is
// reported so the account can be provisioned as an admin.
func LDAPAuthenticate(cfg store.LDAPConfig, username, password string) (*LDAPResult, error) {
	if password == "" {
		return nil, fmt.Errorf("empty password")
	}
	conn, err := ldap.DialURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("ldap dial: %w", err)
	}
	defer conn.Close()
	if cfg.StartTLS {
		if err := conn.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12}); err != nil {
			return nil, fmt.Errorf("ldap starttls: %w", err)
		}
	}

	// Bind as the service account to search (anonymous if no bind DN).
	if cfg.BindDN != "" {
		if err := conn.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			return nil, fmt.Errorf("ldap service bind: %w", err)
		}
	}

	filter := fmt.Sprintf(cfg.UserFilter, ldap.EscapeFilter(username))
	sr := ldap.NewSearchRequest(
		cfg.UserBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 10, false,
		filter, []string{"dn", "memberOf"}, nil,
	)
	res, err := conn.Search(sr)
	if err != nil {
		return nil, fmt.Errorf("ldap search: %w", err)
	}
	if len(res.Entries) != 1 {
		return nil, fmt.Errorf("user %q not found (or not unique)", username)
	}
	entry := res.Entries[0]

	// Bind as the located user to verify the password.
	if err := conn.Bind(entry.DN, password); err != nil {
		return nil, ErrInvalidCreds
	}

	isAdmin := false
	if cfg.AdminGroupDN != "" {
		for _, g := range entry.GetAttributeValues("memberOf") {
			if strings.EqualFold(strings.TrimSpace(g), cfg.AdminGroupDN) {
				isAdmin = true
				break
			}
		}
	}
	return &LDAPResult{Username: username, IsAdmin: isAdmin}, nil
}
