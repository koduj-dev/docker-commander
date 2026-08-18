package docker

import (
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
