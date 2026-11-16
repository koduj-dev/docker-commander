//go:build windows

package docker

import "os"

// writeCheckSupported is false: Windows has no cheap access(2) equivalent, ACLs
// decide. Callers that report write access must say it wasn't checked.
const writeCheckSupported = false

// dirWritable has no cheap equivalent on Windows, where ACLs decide; a failed
// save there reports the real error instead.
func dirWritable(string) error { return nil }

func pathWritable(string) error { return nil }

// openNoBlock is a plain open: Windows has no FIFOs in the filesystem.
func openNoBlock(p string) (*os.File, error) { return os.Open(p) }
