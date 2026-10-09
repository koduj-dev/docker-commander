package api

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The helpers below do the actual file I/O for paths that safeJoin has
// already checked. safeJoin turns a bad name away early with a clear message,
// but it resolves symlinks at check time only: a symlink planted between the
// check and the write (by a container that has the project folder mounted,
// say) would still carry the write outside. Going through an os.Root opened
// on the sandbox closes that gap — the kernel refuses to leave root at the
// moment of use, whatever the tree looks like by then.

// rootRel is full relative to root. full always comes from safeJoin(root, …),
// so it lies inside root.
func rootRel(root, full string) (string, error) {
	return filepath.Rel(root, full)
}

// writeRootFile writes data to rel inside rt, creating parent folders.
func writeRootFile(rt *os.Root, rel string, data []byte, dirMode, fileMode os.FileMode) error {
	if dir := filepath.Dir(rel); dir != "." {
		if err := rt.MkdirAll(dir, dirMode); err != nil {
			return err
		}
	}
	return rt.WriteFile(rel, data, fileMode)
}

// writeInRoot writes data to full (a safeJoin result under root), creating
// root and any parent folders.
func writeInRoot(root, full string, data []byte, dirMode, fileMode os.FileMode) error {
	if err := os.MkdirAll(root, dirMode); err != nil {
		return err
	}
	rel, err := rootRel(root, full)
	if err != nil {
		return err
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rt.Close()
	return writeRootFile(rt, rel, data, dirMode, fileMode)
}

// mkdirInRoot creates the folder full (a safeJoin result under root).
func mkdirInRoot(root, full string, dirMode os.FileMode) error {
	if err := os.MkdirAll(root, dirMode); err != nil {
		return err
	}
	rel, err := rootRel(root, full)
	if err != nil {
		return err
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rt.Close()
	return rt.MkdirAll(rel, dirMode)
}

// openInRoot opens full (a safeJoin result under root) for reading.
func openInRoot(root, full string) (*os.File, error) {
	rel, err := rootRel(root, full)
	if err != nil {
		return nil, err
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rt.Close() // the opened file outlives its Root
	return rt.Open(rel)
}

// removeInRoot removes the file full (a safeJoin result under root).
func removeInRoot(root, full string) error {
	rel, err := rootRel(root, full)
	if err != nil {
		return err
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rt.Close()
	return rt.Remove(rel)
}

// missingInRoot reports whether full (a safeJoin result under root) does not
// exist yet — the file-count cap applies only to new files.
func missingInRoot(root, full string) bool {
	rel, err := rootRel(root, full)
	if err != nil {
		return false
	}
	rt, err := os.OpenRoot(root)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	defer rt.Close()
	_, err = rt.Stat(rel)
	return errors.Is(err, fs.ErrNotExist)
}

// walkRoot walks root through an os.Root, so neither the walk nor a read made
// with fsys can be carried outside by a symlink swapped in mid-walk. rel is
// slash-separated and relative to root ("." for root itself).
func walkRoot(root string, fn func(fsys fs.FS, rel string, d fs.DirEntry, err error) error) error {
	rt, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rt.Close()
	fsys := rt.FS()
	return fs.WalkDir(fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if err == nil && testHookWalkEntry != nil {
			testHookWalkEntry(root, rel)
		}
		return fn(fsys, rel, d, err)
	})
}

// testHookWalkEntry, when set by a test, runs as walkRoot reaches each entry —
// the moment a racing process would swap something in.
var testHookWalkEntry func(root, rel string)
