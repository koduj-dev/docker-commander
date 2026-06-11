//go:build !linux && !darwin

package service

import (
	"errors"
	"io"
)

// errUnsupported explains that self-install isn't wired up for this OS yet.
var errUnsupported = errors.New("`--install-service` is only supported on Linux and macOS; " +
	"on Windows use deploy/install-windows.ps1 (Scheduled Task)")

func Install(io.Writer) error   { return errUnsupported }
func Uninstall(io.Writer) error { return errUnsupported }
func Status(io.Writer) error    { return errUnsupported }
