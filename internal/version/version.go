// Package version compares dotted semantic-version strings (major.minor.patch).
// It is deliberately tiny and dependency-free so both the API update check and
// the CLI self-upgrade can share one comparator.
package version

import (
	"strconv"
	"strings"
)

// Parse returns the major.minor.patch of a version string. A leading "v" and
// any "-pre"/"+build" suffix are ignored. ok is false for unparseable input
// (e.g. "dev").
func Parse(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i := range parts {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// Less reports whether a is strictly older than b. Unparseable versions are
// treated as not-older, so a "dev" build never compares as outdated.
func Less(a, b string) bool {
	av, aok := Parse(a)
	bv, bok := Parse(b)
	if !aok || !bok {
		return false
	}
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			return av[i] < bv[i]
		}
	}
	return false
}
