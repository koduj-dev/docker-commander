package config

import (
	"os"
	"regexp"
	"testing"
)

// flagDef matches a flag registration in config.go and captures the flag name —
// the first string literal, whether the call is flag.Xxx("name", …) or
// flag.XxxVar(&dst, "name", …).
var flagDef = regexp.MustCompile(
	`flag\.(?:String|Bool|Int|Int64|Float64|Duration)(?:Var)?\((?:&[^,]+,\s*)?"([a-zA-Z][\w-]*)"`)

// TestManPageDocumentsAllFlags fails if a flag defined in config.go isn't
// mentioned in deploy/dockercmd.1, so new flags can't ship undocumented. It
// scrapes the source (no flag.CommandLine side effects) the same spirit as the
// service package's unit/man-page sync tests.
func TestManPageDocumentsAllFlags(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	man, err := os.ReadFile("../../deploy/dockercmd.1")
	if err != nil {
		t.Fatalf("read deploy/dockercmd.1: %v", err)
	}
	manStr := string(man)

	matches := flagDef.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no flag definitions found in config.go — has the regex drifted?")
	}
	seen := map[string]bool{}
	for _, m := range matches {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		// Require the roff-escaped option token (\-name) as a whole word, so a
		// short flag like -p can't pass just by being a common letter, and
		// -port doesn't accidentally satisfy -p.
		re := regexp.MustCompile(`\\-` + regexp.QuoteMeta(name) + `\b`)
		if !re.MatchString(manStr) {
			t.Errorf("flag -%s is defined in config.go but not documented (as \\-%s) in deploy/dockercmd.1", name, name)
		}
	}
}
