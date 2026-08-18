//go:build !linux && !darwin && !windows

package service

import (
	"errors"
	"io"
)

// errUnsupported explains that self-install isn't wired up for this OS.
var errUnsupported = errors.New("`--install-service` is only supported on Linux, macOS and Windows")

func Install(io.Writer) error   { return errUnsupported }
func Uninstall(io.Writer) error { return errUnsupported }
func Status(io.Writer) error    { return errUnsupported }
