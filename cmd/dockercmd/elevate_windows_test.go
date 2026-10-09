//go:build windows

package main

import "testing"

// TestQuoteWindowsArgs pins the exact escaping quoteWindowsArgs produces,
// including the case a hand-rolled `"`-only escaper gets wrong: a run of
// backslashes immediately before the closing quote must be doubled, or
// CommandLineToArgvW (what ShellExecute's re-elevated process parses its
// command line with) reads them as escaping that quote instead of being
// literal path separators — see the doc comment on quoteWindowsArgs.
func TestQuoteWindowsArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"single plain arg", []string{"--self-upgrade"}, `--self-upgrade`},
		{"multiple plain args", []string{"--self-upgrade", "--check"}, `--self-upgrade --check`},
		{"arg with a space", []string{"-data-dir", "C:\\Program Files\\dc"}, `-data-dir "C:\Program Files\dc"`},
		{"embedded quote, no space", []string{`say"hi"`}, `say\"hi\"`},
		// Even number of trailing backslashes before the closing quote: they
		// double (each literal backslash becomes two) and the quote still
		// closes the argument normally.
		{"trailing double backslash", []string{`C:\Program Files\dc\\`}, `"C:\Program Files\dc\\\\"`},
		// Odd number of trailing backslashes: same doubling rule, plus the
		// escaped literal backslash from ReplaceAll-equivalent handling —
		// this is exactly the case the naive `"`-only escaper mishandled.
		{"trailing single backslash before implied close", []string{`C:\Program Files\dc\`}, `"C:\Program Files\dc\\"`},
		{"backslash with no adjacent quote or space is untouched", []string{`C:\Users\alice\file.txt`}, `C:\Users\alice\file.txt`},
		{"empty arg still round-trips as an empty argument", []string{""}, `""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteWindowsArgs(tc.args); got != tc.want {
				t.Errorf("quoteWindowsArgs(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
