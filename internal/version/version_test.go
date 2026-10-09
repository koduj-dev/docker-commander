package version

import "testing"

func TestLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.0", "1.3.0", true},
		{"1.2.0", "v1.3.0", true},     // leading v on either side
		{"v1.2.3", "1.2.4", true},     // patch bump
		{"1.2.0", "2.0.0", true},      // major bump
		{"1.9.0", "1.10.0", true},     // numeric, not lexical, compare
		{"1.3.0", "1.3.0", false},     // equal
		{"1.3.0", "1.2.9", false},     // newer than latest
		{"2.0.0", "1.9.9", false},     // newer major
		{"dev", "1.3.0", false},       // dev build never nags
		{"1.3.0", "weird", false},     // unparseable latest
		{"1.2", "1.3", true},          // short forms
		{"1.2.0-rc1", "1.2.0", false}, // pre-release suffix stripped → equal
	}
	for _, c := range cases {
		if got := Less(c.a, c.b); got != c.want {
			t.Errorf("Less(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDelta(t *testing.T) {
	cases := []struct {
		a, b string
		want string
	}{
		{"1.2.3", "2.0.0", "major"},
		{"1.2.3", "1.3.0", "minor"},
		{"1.2.3", "1.2.4", "patch"},
		{"1.2.3", "1.2.3", ""},        // equal
		{"1.3.0", "1.2.9", ""},        // b older than a
		{"dev", "1.3.0", ""},          // unparseable a
		{"1.3.0", "weird", ""},        // unparseable b
		{"v1.2.3", "v2.0.0", "major"}, // leading v on both sides
	}
	for _, c := range cases {
		if got := Delta(c.a, c.b); got != c.want {
			t.Errorf("Delta(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
