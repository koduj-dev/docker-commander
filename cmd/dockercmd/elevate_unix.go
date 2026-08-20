//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// elevateAndRerun replaces this process with `sudo <self> <same args>`,
// preserving the terminal for the sudo password prompt and, once granted,
// for the elevated self-upgrade's own progress output. Only returns on
// failure to launch sudo — on success the process image is replaced, the
// same way reexecSelf() replaces it after a completed self-update.
func elevateAndRerun() error {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("sudo not found in PATH: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	argv := append([]string{sudo, exe}, os.Args[1:]...)
	return syscall.Exec(sudo, argv, os.Environ())
}
