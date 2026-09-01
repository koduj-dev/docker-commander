package store

import (
	"context"
	"encoding/json"
)

// PolicyRuleIDs are the deploy-time policy checks a mode can be set for.
// Duplicated (not imported) from internal/docker.PolicyRuleID's string
// values, since internal/docker already imports this package and importing
// back would cycle — internal/api's
// TestPolicyRuleIDsMatchBetweenStoreAndDocker keeps the two lists in sync.
var PolicyRuleIDs = []string{
	"privileged",
	"host_network",
	"host_pid",
	"docker_socket_mount",
	"latest_tag",
	"missing_resource_limits",
	"missing_healthcheck",
}

// PolicyModes are the valid values for each rule.
var PolicyModes = []string{"off", "warn", "block"}

const policyRuleModesKey = "policy_rule_modes"

func validPolicyRuleID(id string) bool {
	for _, r := range PolicyRuleIDs {
		if r == id {
			return true
		}
	}
	return false
}

func validPolicyMode(mode string) bool {
	for _, m := range PolicyModes {
		if m == mode {
			return true
		}
	}
	return false
}

// PolicyRuleModes returns the configured mode for every known rule, defaulting
// an unset rule to "off". This engine is opt-in: most existing compose files
// have no healthcheck or resource limits, so defaulting even to "warn" would
// make nearly every deploy across every existing install suddenly demand an
// extra confirmation the admin never asked for. An admin turns on the rules
// they actually want enforced.
func (s *Store) PolicyRuleModes(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(PolicyRuleIDs))
	for _, id := range PolicyRuleIDs {
		out[id] = "off"
	}
	raw, err := s.Setting(ctx, policyRuleModesKey)
	if err != nil {
		return out, err
	}
	if raw == "" {
		return out, nil
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return out, nil
	}
	for id, mode := range stored {
		if validPolicyRuleID(id) && validPolicyMode(mode) {
			out[id] = mode
		}
	}
	return out, nil
}

// SetPolicyRuleModes persists a mode per rule, silently dropping any unknown
// rule id or mode value rather than rejecting the whole update.
func (s *Store) SetPolicyRuleModes(ctx context.Context, modes map[string]string) error {
	clean := make(map[string]string, len(modes))
	for id, mode := range modes {
		if validPolicyRuleID(id) && validPolicyMode(mode) {
			clean[id] = mode
		}
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, policyRuleModesKey, string(b))
}
