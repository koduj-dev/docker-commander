package docker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var errNotRegular = errors.New("not a regular file")

// StackFileAccess is what Docker Commander itself can do with one stack's
// compose file on the local host: open it (view, redeploy) and save it.
type StackFileAccess struct {
	Project  string `json:"project"`
	Path     string `json:"path"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
	// Reason says why the file can't be read, or else why it can't be saved,
	// with the likely cause and fix (see localAccessHint).
	Reason string `json:"reason,omitempty"`
}

// LocalStackFileAccess checks every stack on hostID, which must be the local
// host, for the access this process has to its compose file. It answers what
// opening each stack would, without reading any file's content: an open to
// prove read access, access(2) on the directory for write access (Save
// renames a temp file over it). Stacks that record no compose file are left
// out, as the Stacks page offers nothing for them either.
//
// It lists the containers once for all stacks, instead of resolving each stack
// on its own the way opening one does.
func (m *Manager) LocalStackFileAccess(ctx context.Context, hostID int64) ([]StackFileAccess, error) {
	stacks, err := m.ListStacks(ctx, hostID)
	if err != nil {
		return nil, err
	}
	out := make([]StackFileAccess, 0, len(stacks))
	for i := range stacks {
		if stacks[i].ConfigFile == "" {
			continue
		}
		out = append(out, stackFileAccess(&stacks[i]))
	}
	return out, nil
}

// stackFileAccess resolves the compose file the way resolveStack does (the
// first YAML candidate that opens wins) and checks it.
func stackFileAccess(st *Stack) StackFileAccess {
	a := StackFileAccess{Project: st.Project}
	var lastErr error
	for _, p := range composeCandidates(st) {
		base := strings.ToLower(filepath.Base(p))
		if !strings.HasSuffix(base, ".yml") && !strings.HasSuffix(base, ".yaml") {
			continue
		}
		if a.Path == "" {
			a.Path = p // reported even when nothing opens
		}
		// Only a regular file is opened. The path comes from a container label,
		// and opening a FIFO given a .yml name would block this check, and with
		// it the whole diagnostics request, until something writes to it.
		fi, err := os.Stat(p)
		if err != nil {
			lastErr = err
			continue
		}
		if !fi.Mode().IsRegular() {
			lastErr = errNotRegular
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			lastErr = err
			continue
		}
		_ = f.Close()
		a.Path, a.Readable = p, true
		break
	}
	if !a.Readable {
		if errors.Is(lastErr, errNotRegular) {
			a.Reason = "its compose path isn't a regular file, so Docker Commander won't open it"
		} else if lastErr != nil {
			a.Reason = localAccessHint(lastErr).Error()
		} else {
			a.Reason = "its compose file isn't a .yml/.yaml path, so Docker Commander won't open it"
		}
		return a
	}
	dir := filepath.Dir(a.Path)
	if err := dirWritable(dir); err != nil {
		a.Reason = localAccessHint(&fs.PathError{Op: "write", Path: dir, Err: err}).Error()
		return a
	}
	a.Writable = true
	return a
}

// PathWritable reports, without writing anything, whether this process may
// write path (a file, or a directory to create files in). nil on Windows,
// where the access(2)-style check doesn't exist.
func PathWritable(path string) error { return dirWritable(path) }
