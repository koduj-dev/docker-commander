//go:build !windows

package docker

import "golang.org/x/sys/unix"

// dirWritable reports whether this process may create files in dir. access(2)
// answers for the real uid and also returns EROFS on a read-only mount, which
// is what systemd's ProtectSystem=strict makes of the tree outside
// ReadWritePaths.
func dirWritable(dir string) error {
	return unix.Access(dir, unix.W_OK)
}
