package store

import (
	"context"
	"encoding/json"
	"time"
)

// Sections are the access-control units, matching the app's menu. A user's
// permissions and the global feature flags are both expressed as sets of these.
var Sections = []string{
	"dashboard", "containers", "projects", "images", "volumes", "networks", "topology",
	"logs", "events", "alerts", "hosts", "registries", "audit", "diagnostics",
}

// ValidSection reports whether key is a known section.
func ValidSection(key string) bool {
	for _, s := range Sections {
		if s == key {
			return true
		}
	}
	return false
}

const (
	disabledSectionsKey = "disabled_sections"
	localhostNo2FAKey   = "localhost_no_2fa"
	selfUpdatePolicyKey = "self_update_policy"
	lastAutoUpdateKey   = "last_auto_update"
)

// DisabledSections returns the sections an admin has turned off app-wide.
func (s *Store) DisabledSections(ctx context.Context) ([]string, error) {
	raw, err := s.Setting(ctx, disabledSectionsKey)
	if err != nil || raw == "" {
		return []string{}, err
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out, nil
}

// SetDisabledSections persists the app-wide disabled sections.
func (s *Store) SetDisabledSections(ctx context.Context, keys []string) error {
	clean := make([]string, 0, len(keys))
	for _, k := range keys {
		if ValidSection(k) {
			clean = append(clean, k)
		}
	}
	b, _ := json.Marshal(clean)
	return s.SetSetting(ctx, disabledSectionsKey, string(b))
}

// LocalhostNo2FA reports whether password-only login is allowed from loopback.
func (s *Store) LocalhostNo2FA(ctx context.Context) (bool, error) {
	raw, err := s.Setting(ctx, localhostNo2FAKey)
	return raw == "1", err
}

// SetLocalhostNo2FA toggles the localhost 2FA exemption.
func (s *Store) SetLocalhostNo2FA(ctx context.Context, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.SetSetting(ctx, localhostNo2FAKey, v)
}

// SelfUpdatePolicy controls whether the server applies a newer release of
// itself automatically instead of waiting for an admin to click "Update &
// restart". Granularity is a ceiling, not an exact match: "minor" allows
// both minor and patch releases through, "major" allows everything.
type SelfUpdatePolicy struct {
	Enabled     bool   `json:"enabled"`
	Granularity string `json:"granularity"` // "patch" | "minor" | "major"
}

// SelfUpdatePolicy returns the current auto-apply policy. Off by default
// when the key has never been set.
func (s *Store) SelfUpdatePolicy(ctx context.Context) (SelfUpdatePolicy, error) {
	raw, err := s.Setting(ctx, selfUpdatePolicyKey)
	if err != nil || raw == "" {
		return SelfUpdatePolicy{}, err
	}
	var out SelfUpdatePolicy
	_ = json.Unmarshal([]byte(raw), &out)
	return out, nil
}

// SetSelfUpdatePolicy persists the auto-apply policy.
func (s *Store) SetSelfUpdatePolicy(ctx context.Context, p SelfUpdatePolicy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, selfUpdatePolicyKey, string(b))
}

// LastAutoUpdate records the most recent policy-driven self-update, so every
// admin sees a one-time "you're now on vX.Y.Z" notice at next login.
type LastAutoUpdate struct {
	Version   string    `json:"version"`
	AppliedAt time.Time `json:"appliedAt"`
}

// LastAutoUpdate returns the last automatic self-update, if any.
func (s *Store) LastAutoUpdate(ctx context.Context) (*LastAutoUpdate, error) {
	raw, err := s.Setting(ctx, lastAutoUpdateKey)
	if err != nil || raw == "" {
		return nil, err
	}
	var out LastAutoUpdate
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, nil
	}
	return &out, nil
}

// SetLastAutoUpdate persists the most recent automatic self-update.
func (s *Store) SetLastAutoUpdate(ctx context.Context, v LastAutoUpdate) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, lastAutoUpdateKey, string(b))
}
