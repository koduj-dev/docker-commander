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

// envVar matches an environment variable name as config.go spells it.
var envVar = regexp.MustCompile(`"(DC_[A-Z][A-Z0-9_]*)"`)

// TestDocsListAllEnvVars fails if a DC_* variable read in config.go is missing
// from the man page, the example config or the README table, the three places an
// operator looks a setting up. DC_DEV is left out of the example config on
// purpose: it is not something to set on a real install.
func TestDocsListAllEnvVars(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	docs := map[string]string{}
	for _, p := range []string{"../../deploy/dockercmd.1", "../../deploy/commander.conf.example", "../../README.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		docs[p] = string(b)
	}
	names := map[string]bool{}
	for _, m := range envVar.FindAllStringSubmatch(string(src), -1) {
		names[m[1]] = true
	}
	if len(names) < 20 {
		t.Fatalf("found only %d DC_* variables in config.go — has the regex drifted?", len(names))
	}
	for name := range names {
		re := regexp.MustCompile(`\b` + name + `\b`)
		for p, doc := range docs {
			if name == "DC_DEV" && p == "../../deploy/commander.conf.example" {
				continue
			}
			if !re.MatchString(doc) {
				t.Errorf("%s is read in config.go but not documented in %s", name, p)
			}
		}
	}
}
