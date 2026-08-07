package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The README's test counts rot silently.
//
// They said "~533 Go unit tests and 73 frontend tests" for five commits while the
// real numbers were ~700 and ~140 — nobody notices a number that merely looks
// plausible. The claims are worth keeping (they say something true about the shape
// of the project), so they get a guard instead of being deleted.
//
// The first version of this guard checked only the Go figure, at a quarter either
// way. Both halves of that were wrong. Two of the three numbers were unguarded, so
// the README could claim 73 frontend tests and 7 pentest cases with the suite
// green. And a quarter is wider than the drift it was written for: at 700 real
// tests it accepts anything from 525, so the 533 it cites as its own motivating
// example would have sailed through. A tenth catches that (630) while still
// absorbing ordinary work — at today's counts it takes ~60 new Go tests to trip.
//
// It counts declarations rather than running anything, so it stays fast and needs
// no Docker or npm.
func TestReadmeTestCountsAreNotStale(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join("..", "..")
	for _, c := range []struct {
		what  string
		claim *regexp.Regexp
		count func() (int, error)
	}{
		{
			// Unit tests only: the pentest cases are counted separately below, and
			// the README presents them as a distinct tier rather than a subset.
			what:  "Go unit tests",
			claim: regexp.MustCompile(`~([0-9]+) Go unit tests`),
			count: func() (int, error) { return countGoTests(root, false) },
		},
		{
			what:  "frontend tests",
			claim: regexp.MustCompile(`~([0-9]+) frontend tests`),
			count: func() (int, error) { return countFrontendTests(root) },
		},
		{
			what:  "pentest cases",
			claim: regexp.MustCompile(`([0-9]+) adversarial`),
			count: func() (int, error) { return countGoTests(root, true) },
		},
	} {
		t.Run(c.what, func(t *testing.T) {
			m := c.claim.FindSubmatch(readme)
			if m == nil {
				t.Skipf("the README no longer states a %s count; nothing to keep honest", c.what)
			}
			claimed, err := strconv.Atoi(string(m[1]))
			if err != nil {
				t.Fatalf("unreadable %s figure %q: %v", c.what, m[1], err)
			}

			actual, err := c.count()
			if err != nil {
				t.Fatal(err)
			}
			if actual == 0 {
				t.Fatalf("counted zero %s; the count is broken, not the README", c.what)
			}

			// A tenth either way. See the note above for why not a quarter.
			lo, hi := actual*9/10, actual*11/10
			if claimed < lo || claimed > hi {
				t.Errorf("README claims %d %s; there are %d. "+
					"Update the figure (anything from %d to %d passes).",
					claimed, c.what, actual, lo, hi)
			}
		})
	}
}

// isPentestFile reports whether a Go test file holds the adversarial tier. The
// repo marks those by filename (…_pentest_test.go, pentest_test.go).
func isPentestFile(name string) bool {
	return regexp.MustCompile(`pentest.*_test\.go$`).MatchString(name)
}

// countGoTests counts top-level Test functions, in either the pentest files or
// everything but them, so the two figures never double-count each other.
func countGoTests(root string, pentest bool) (int, error) {
	fn := regexp.MustCompile(`(?m)^func Test[A-Z_]`)
	total := 0
	err := walkSource(root, func(path string, src []byte) {
		if filepath.Ext(path) != ".go" || !regexp.MustCompile(`_test\.go$`).MatchString(path) {
			return
		}
		if isPentestFile(filepath.Base(path)) != pentest {
			return
		}
		total += len(fn.FindAll(src, -1))
	})
	return total, err
}

// countFrontendTests counts Vitest cases by declaration. This is a floor, not an
// exact figure: `it.each(...)` expands into several cases at run time, so the
// runtime total sits a little above this. The tolerance absorbs the difference.
func countFrontendTests(root string) (int, error) {
	decl := regexp.MustCompile(`(?m)^\s*(it|test)(\.\w+)?\(`)
	isTest := regexp.MustCompile(`\.test\.(ts|tsx|js|jsx)$`)
	total := 0
	err := walkSource(filepath.Join(root, "web", "src"), func(path string, src []byte) {
		if !isTest.MatchString(path) {
			return
		}
		total += len(decl.FindAll(src, -1))
	})
	return total, err
}

// walkSource visits every file under root, skipping dependency trees and hidden
// directories. A throwaway git worktree under .claude/ holds a whole second copy
// of the repo, and counting it doubled the figures — the test would then pass or
// fail depending on whether somebody happened to leave one lying around.
func walkSource(root string, visit func(path string, src []byte)) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); path != root &&
				(name == "node_modules" || (len(name) > 1 && name[0] == '.')) {
				return filepath.SkipDir
			}
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(path, src)
		return nil
	})
}
