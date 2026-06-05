package store

import (
	"context"
	"encoding/json"
)

// Sections are the access-control units, matching the app's menu. A user's
// permissions and the global feature flags are both expressed as sets of these.
var Sections = []string{
	"dashboard", "containers", "projects", "images", "volumes", "networks", "topology",
	"logs", "events", "alerts", "hosts", "registries", "audit",
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
