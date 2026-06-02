package auth

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// seedUser adds a searchable user (uid=alice) to the directory; the rootdn
// (cn=admin) has no DIT entry and isn't returned by searches, so we need a real
// inetOrgPerson to exercise the search → user-bind path.
const seedLDIF = `dn: ou=people,dc=example,dc=org
objectClass: organizationalUnit
ou: people

dn: uid=alice,ou=people,dc=example,dc=org
objectClass: inetOrgPerson
cn: alice
sn: Test
uid: alice
userPassword: secret123
`

// startOpenLDAP runs an osixia/openldap container seeded with the default
// example.org directory (admin DN cn=admin,dc=example,dc=org / "admin") and
// returns a config pointing at it. Skipped when Docker/the image is absent.
func startOpenLDAP(t *testing.T) store.LDAPConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("ldap integration test; skipped under -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-p", "127.0.0.1::389",
		"--env", "LDAP_ORGANISATION=Example",
		"--env", "LDAP_DOMAIN=example.org",
		"--env", "LDAP_ADMIN_PASSWORD=admin",
		"osixia/openldap:1.5.0").CombinedOutput()
	if err != nil {
		t.Skipf("cannot start openldap: %v (%s)", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	portOut, err := exec.Command("docker", "port", id, "389/tcp").Output()
	if err != nil {
		t.Skipf("cannot read mapped port: %v", err)
	}
	addr := strings.TrimSpace(strings.SplitN(string(portOut), "\n", 2)[0])

	// Wait until slapd accepts the admin bind, then seed a user.
	for i := 0; i < 40; i++ {
		add := exec.Command("docker", "exec", "-i", id, "ldapadd", "-x",
			"-H", "ldap://localhost", "-D", "cn=admin,dc=example,dc=org", "-w", "admin")
		add.Stdin = strings.NewReader(seedLDIF)
		if out, err := add.CombinedOutput(); err == nil {
			break
		} else if i == 39 {
			t.Skipf("could not seed ldap user: %v (%s)", err, out)
		}
		time.Sleep(500 * time.Millisecond)
	}

	return store.LDAPConfig{
		URL:          "ldap://" + addr,
		BindDN:       "cn=admin,dc=example,dc=org",
		BindPassword: "admin",
		UserBaseDN:   "dc=example,dc=org",
		UserFilter:   "(uid=%s)",
	}
}

func TestLDAPIntegration(t *testing.T) {
	cfg := startOpenLDAP(t)

	// openldap needs a few seconds to accept connections.
	var n int
	var err error
	for i := 0; i < 40; i++ {
		n, err = LDAPTest(cfg)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("LDAPTest never succeeded: %v", err)
	}
	if n < 1 {
		t.Errorf("expected at least one entry under the base, got %d", n)
	}

	// Authenticate the seeded user (search by uid, then bind as that DN).
	res, err := LDAPAuthenticate(cfg, "alice", "secret123")
	if err != nil {
		t.Fatalf("LDAPAuthenticate(alice): %v", err)
	}
	if res.Username != "alice" {
		t.Errorf("unexpected result: %+v", res)
	}

	// Wrong password → invalid creds.
	if _, err := LDAPAuthenticate(cfg, "alice", "wrong"); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("wrong password should be ErrInvalidCreds, got %v", err)
	}
	// Unknown user → not found (non-nil error, not a panic).
	if _, err := LDAPAuthenticate(cfg, "ghost", "x"); err == nil {
		t.Error("unknown user should error")
	}
	// Empty password is rejected before any bind.
	if _, err := LDAPAuthenticate(cfg, "admin", ""); err == nil {
		t.Error("empty password should error")
	}
}
