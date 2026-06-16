package auth

import (
	"sort"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func cfgWith(mappings ...store.LDAPGroupMapping) store.LDAPConfig {
	return store.LDAPConfig{GroupMappings: mappings}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestSectionsForGroups_UnionAcrossGroups(t *testing.T) {
	cfg := cfgWith(
		store.LDAPGroupMapping{GroupDN: "cn=devops,ou=groups,dc=ex,dc=org", Sections: []string{"containers", "logs"}},
		store.LDAPGroupMapping{GroupDN: "cn=netadmin,ou=groups,dc=ex,dc=org", Sections: []string{"networks", "logs"}},
	)
	got := SectionsForGroups(cfg, []string{
		"cn=devops,ou=groups,dc=ex,dc=org",
		"cn=netadmin,ou=groups,dc=ex,dc=org",
	})
	want := []string{"containers", "logs", "networks"} // logs de-duplicated
	if a, b := sorted(got), sorted(want); len(a) != len(b) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, v := range sorted(want) {
		if sorted(got)[i] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSectionsForGroups_CaseAndWhitespaceInsensitive(t *testing.T) {
	cfg := cfgWith(store.LDAPGroupMapping{GroupDN: "CN=DevOps,OU=Groups,DC=Ex,DC=Org", Sections: []string{"images"}})
	got := SectionsForGroups(cfg, []string{"  cn=devops,ou=groups,dc=ex,dc=org  "})
	if len(got) != 1 || got[0] != "images" {
		t.Errorf("DN match should be case/whitespace insensitive, got %v", got)
	}
}

// PENTEST: a group the user is NOT a member of must grant nothing, and a
// substring/partial DN must not match (no escalation by prefix collision).
func TestSectionsForGroups_NoMembershipNoGrant(t *testing.T) {
	cfg := cfgWith(store.LDAPGroupMapping{GroupDN: "cn=admins,ou=groups,dc=ex,dc=org", Sections: []string{"containers"}})
	// Member of a *different* group whose DN is a prefix of the mapped one.
	if got := SectionsForGroups(cfg, []string{"cn=admins,ou=groups,dc=ex"}); len(got) != 0 {
		t.Errorf("partial DN must not match, got %v", got)
	}
	if got := SectionsForGroups(cfg, []string{"cn=other,ou=groups,dc=ex,dc=org"}); len(got) != 0 {
		t.Errorf("non-member must get no sections, got %v", got)
	}
	if got := SectionsForGroups(cfg, nil); len(got) != 0 {
		t.Errorf("no groups must get no sections, got %v", got)
	}
}

// PENTEST: an unknown/bogus section name in a mapping must be ignored, so a
// crafted config can't grant access to a non-section (or a future-privileged
// keyword).
func TestSectionsForGroups_DropsUnknownSections(t *testing.T) {
	cfg := cfgWith(store.LDAPGroupMapping{
		GroupDN:  "cn=x,dc=ex",
		Sections: []string{"containers", "__admin", "root", "logs"},
	})
	got := sorted(SectionsForGroups(cfg, []string{"cn=x,dc=ex"}))
	want := []string{"containers", "logs"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("unknown sections must be dropped: got %v, want %v", got, want)
	}
}

func TestSectionsForGroups_NoMappingsConfigured(t *testing.T) {
	// No mappings → nil (caller then preserves manually-managed sections).
	if got := SectionsForGroups(store.LDAPConfig{}, []string{"cn=x,dc=ex"}); got != nil {
		t.Errorf("expected nil with no mappings, got %v", got)
	}
}

func TestSameSections(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a"}, []string{"a", "b"}, false},
		{nil, nil, true},
		{nil, []string{"a"}, false},
		{[]string{"a", "b"}, []string{"a", "c"}, false},
	}
	for _, c := range cases {
		if got := sameSections(c.a, c.b); got != c.want {
			t.Errorf("sameSections(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
