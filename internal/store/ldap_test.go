package store

import "testing"

func TestSetLDAP_GroupMappingsRoundTripAndClean(t *testing.T) {
	s, ctx := newStore(t)

	in := LDAPConfig{
		Enabled: true, URL: "ldap://x:389", UserBaseDN: "dc=ex", UserFilter: "(uid=%s)",
		GroupMappings: []LDAPGroupMapping{
			{
				GroupDN:  "  cn=devops,dc=ex  ",
				Sections: []string{"containers", "bogus", "logs", "containers"},
				RoleIDs:  []int64{3, 0, 3, -1, 5},
			},
			{GroupDN: "", Sections: []string{"images"}}, // blank DN → dropped
		},
	}
	if err := s.SetLDAP(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := s.GetLDAP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.GroupMappings) != 1 {
		t.Fatalf("expected 1 mapping after cleaning, got %d (%+v)", len(out.GroupMappings), out.GroupMappings)
	}
	m := out.GroupMappings[0]
	if m.GroupDN != "cn=devops,dc=ex" {
		t.Errorf("group DN should be trimmed, got %q", m.GroupDN)
	}
	// "bogus" dropped (not a section), "containers" de-duplicated.
	want := []string{"containers", "logs"}
	if len(m.Sections) != len(want) || m.Sections[0] != want[0] || m.Sections[1] != want[1] {
		t.Errorf("sections = %v, want %v (unknown dropped, dupes removed)", m.Sections, want)
	}
	// Role ids: zero and negative are not ids, and a repeat grants nothing extra.
	if len(m.RoleIDs) != 2 || m.RoleIDs[0] != 3 || m.RoleIDs[1] != 5 {
		t.Errorf("roleIds = %v, want [3 5]", m.RoleIDs)
	}
}
