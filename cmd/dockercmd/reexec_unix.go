//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// restartSupported reports whether the in-app restart can re-exec the binary.
const restartSupported = true

// reexecSelf replaces the current process image with the on-disk binary,
// preserving the PID, arguments and environment. After a successful
// --self-update swap this boots the new version with no supervisor required.
// It only returns on failure (on success the image is replaced).
func reexecSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
