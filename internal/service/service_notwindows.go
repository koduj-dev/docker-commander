//go:build !windows

package service

import (
	"context"
	"errors"
)

// IsWindowsService always reports false outside Windows: there is no Service
// Control Manager to have started this process.
func IsWindowsService() bool { return false }

// RunWindowsService only does something on Windows (service_windows.go). This
// stub exists so cmd/dockercmd can call it unconditionally across platforms;
// it is never actually invoked here because IsWindowsService() is always
// false on this OS.
func RunWindowsService(func(context.Context) error) error {
	return errors.New("RunWindowsService is only supported on Windows")
}
