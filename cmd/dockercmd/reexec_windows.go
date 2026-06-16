//go:build windows

package main

import "errors"

// restartSupported is false on Windows: syscall.Exec isn't available and a
// running .exe can't replace its own image, so the in-app restart endpoint is
// not offered and the admin restarts the service manually.
const restartSupported = false

func reexecSelf() error {
	return errors.New("in-app restart is not supported on Windows; restart the service manually")
}
