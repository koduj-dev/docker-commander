package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestNormalizeProfiles is the DC-COR-005 regression: whitespace, exact
// duplicates and empty entries must collapse to one canonical value — the
// same one ComposeUpFiles turns into --profile flags — so a caller that
// persists/audits this value can't disagree with what the CLI actually ran.
func TestNormalizeProfiles(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{}},
		{"empty", []string{}, []string{}},
		{"already clean", []string{"prod", "extra"}, []string{"prod", "extra"}},
		{"trims whitespace", []string{" prod ", "\textra\n"}, []string{"prod", "extra"}},
		{"drops empty/whitespace-only entries", []string{"prod", "", "  ", "extra"}, []string{"prod", "extra"}},
		{"dedupes, keeping first-occurrence order", []string{"prod", "extra", "prod"}, []string{"prod", "extra"}},
		{"the exact review case", []string{" prod ", "prod", ""}, []string{"prod"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeProfiles(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NormalizeProfiles(%#v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// serviceNames unmarshals just enough of a `compose config --format json`
// document to list which services it resolved.
func serviceNames(t *testing.T, cfgJSON []byte) map[string]bool {
	t.Helper()
	var doc struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(cfgJSON, &doc); err != nil {
		t.Fatalf("parse compose config: %v\n%s", err, cfgJSON)
	}
	out := make(map[string]bool, len(doc.Services))
	for name := range doc.Services {
		out[name] = true
	}
	return out
}

// TestComposeConfigJSONFiles_ResolvesProfileGatedServices is the real-CLI
// proof for the P1 policy fix: Compose silently omits a service gated behind
// an inactive profile from a plain `config --format json` (ComposeConfigJSON,
// zero profiles), but ComposeConfigJSONFiles with the profile selected must
// resolve it — exactly the model ComposeUpFiles will deploy under the same
// selection.
func TestComposeConfigJSONFiles_ResolvesProfileGatedServices(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}
	dir := t.TempDir()
	compose := `services:
  web:
    image: alpine:latest
  danger:
    image: alpine:latest
    profiles: ["danger"]
    privileged: true
`
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	const slug = "policy-profile-resolve"

	plain, err := ComposeConfigJSON(ctx, dir, slug)
	if err != nil {
		t.Fatalf("resolve plain config: %v", err)
	}
	if names := serviceNames(t, plain); names["danger"] {
		t.Fatalf("a plain (no-profile) resolve should omit the profile-gated service, got %v", names)
	}

	withProfile, err := ComposeConfigJSONFiles(ctx, dir, slug, []string{"danger"}, nil, nil)
	if err != nil {
		t.Fatalf("resolve with profile: %v", err)
	}
	if names := serviceNames(t, withProfile); !names["danger"] {
		t.Fatalf("ComposeConfigJSONFiles with the selected profile must include the gated service, got %v", names)
	}
}

// TestComposeConfigJSONFiles_NeutralizesInheritedComposeProfiles is
// ComposeConfigJSONFiles' half of the guarantee composeUp already gives
// ComposeUpFiles (see TestHandleDeployProject_EnvFileComposeProfilesDoesNotLeakIn
// in internal/api): a COMPOSE_PROFILES value on the calling process's own
// environment must not activate a profile the caller didn't select, or a
// policy check could pass against fewer services than compose actually
// resolves once COMPOSE_PROFILES leaks into the real `up`.
func TestComposeConfigJSONFiles_NeutralizesInheritedComposeProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}
	t.Setenv("COMPOSE_PROFILES", "danger")
	dir := t.TempDir()
	compose := `services:
  danger:
    image: alpine:latest
    profiles: ["danger"]
`
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	const slug = "policy-profile-env-leak"

	cfgJSON, err := ComposeConfigJSONFiles(ctx, dir, slug, nil, nil, nil)
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if names := serviceNames(t, cfgJSON); names["danger"] {
		t.Fatalf("a process-inherited COMPOSE_PROFILES must not activate a profile the caller didn't select, got %v", names)
	}
}
