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
// plausible. The claim is worth keeping (it says something true about the shape of
// the project), so it gets a guard instead of being deleted.
//
// Deliberately loose: the point is to catch drift of the kind that accumulated
// here, not to force an edit on every added test. It counts declarations rather
// than running anything, so it stays fast and needs no Docker.
func TestReadmeTestCountsAreNotStale(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	claim := regexp.MustCompile(`~([0-9]+) Go unit tests`).FindSubmatch(readme)
	if claim == nil {
		t.Skip("the README no longer states a Go test count; nothing to keep honest")
	}
	claimed, _ := strconv.Atoi(string(claim[1]))

	actual := 0
	err = filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden directories and dependency trees. A throwaway git
			// worktree under .claude/ holds a whole second copy of the repo, and
			// counting it doubled the figure — the test would then pass or fail
			// depending on whether somebody happened to leave one lying around.
			if name := info.Name(); path != filepath.Join("..", "..") &&
				(name == "node_modules" || (len(name) > 1 && name[0] == '.')) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || len(path) < 8 || path[len(path)-8:] != "_test.go" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		actual += len(regexp.MustCompile(`(?m)^func Test[A-Z_]`).FindAll(src, -1))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if actual == 0 {
		t.Fatal("counted zero test functions; the count is broken, not the README")
	}

	// A quarter either way. Wide enough that ordinary work does not trip it,
	// narrow enough that the 533-vs-700 drift would have.
	lo, hi := actual*3/4, actual*5/4
	if claimed < lo || claimed > hi {
		t.Errorf("README claims ~%d Go unit tests; there are %d top-level Test funcs. "+
			"Update the figure (anything from %d to %d passes).", claimed, actual, lo, hi)
	}
}
