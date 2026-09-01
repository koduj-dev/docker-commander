package store

import "testing"

func TestPolicyRuleModes_DefaultsToOffForEveryRule(t *testing.T) {
	s, ctx := newStore(t)
	modes, err := s.PolicyRuleModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) != len(PolicyRuleIDs) {
		t.Fatalf("expected a mode for every known rule, got %d of %d", len(modes), len(PolicyRuleIDs))
	}
	for _, id := range PolicyRuleIDs {
		if modes[id] != "off" {
			t.Errorf("rule %q default = %q, want \"off\"", id, modes[id])
		}
	}
}

func TestSetPolicyRuleModes_RoundTrips(t *testing.T) {
	s, ctx := newStore(t)
	if err := s.SetPolicyRuleModes(ctx, map[string]string{
		"privileged": "block",
		"latest_tag": "off",
	}); err != nil {
		t.Fatal(err)
	}
	modes, err := s.PolicyRuleModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if modes["privileged"] != "block" {
		t.Errorf("privileged = %q, want block", modes["privileged"])
	}
	if modes["latest_tag"] != "off" {
		t.Errorf("latest_tag = %q, want off", modes["latest_tag"])
	}
	// Untouched rules keep their default.
	if modes["host_network"] != "off" {
		t.Errorf("host_network = %q, want off (untouched default)", modes["host_network"])
	}
}

func TestSetPolicyRuleModes_DropsUnknownRuleID(t *testing.T) {
	s, ctx := newStore(t)
	if err := s.SetPolicyRuleModes(ctx, map[string]string{
		"privileged":    "block",
		"not_a_rule_id": "block",
	}); err != nil {
		t.Fatal(err)
	}
	modes, err := s.PolicyRuleModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := modes["not_a_rule_id"]; ok {
		t.Error("an unknown rule id must not be persisted")
	}
	if modes["privileged"] != "block" {
		t.Errorf("privileged = %q, want block", modes["privileged"])
	}
}

func TestSetPolicyRuleModes_DropsUnknownMode(t *testing.T) {
	s, ctx := newStore(t)
	if err := s.SetPolicyRuleModes(ctx, map[string]string{
		"privileged": "delete-everything",
	}); err != nil {
		t.Fatal(err)
	}
	modes, err := s.PolicyRuleModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if modes["privileged"] != "off" {
		t.Errorf("an invalid mode must be dropped, leaving the default; got %q", modes["privileged"])
	}
}
