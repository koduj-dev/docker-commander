//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// swShowNormal is Win32's SW_SHOWNORMAL — a plain visible window for the
// elevated console that ShellExecute opens.
const swShowNormal = 1

// elevateAndRerun launches a NEW elevated instance of this process via
// ShellExecute's "runas" verb (the standard way to trigger a UAC consent
// prompt on Windows) and lets this unelevated process exit — unlike Unix's
// syscall.Exec, Windows has no way to replace the current process's own
// image, so this can only hand off to a second process, not become it.
func elevateAndRerun() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	args, err := windows.UTF16PtrFromString(quoteWindowsArgs(os.Args[1:]))
	if err != nil {
		return err
	}
	cwd := "."
	if wd, err := os.Getwd(); err == nil {
		cwd = wd
	}
	cwdPtr, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return err
	}
	if err := windows.ShellExecute(0, verb, file, args, cwdPtr, swShowNormal); err != nil {
		return fmt.Errorf("elevation was cancelled or failed: %w", err)
	}
	fmt.Println("Elevated self-upgrade launched in a new window; this one will now exit.")
	os.Exit(0)
	return nil // unreachable
}

// quoteWindowsArgs joins args into a single command-line string, quoting any
// argument that contains whitespace or an embedded quote — the same minimal
// quoting CreateProcess/ShellExecute expect, since there is no argv array
// here, just one string the shell re-splits.
func quoteWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		quoted[i] = a
	}
	return strings.Join(quoted, " ")
}
