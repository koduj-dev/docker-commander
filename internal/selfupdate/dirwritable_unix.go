//go:build !windows

package selfupdate

import "golang.org/x/sys/unix"

// dirWritable reports whether this process may create files in dir, without
// creating one. access(2) also returns EROFS on a read-only mount, which is
// what systemd's ProtectSystem=strict makes of the tree outside ReadWritePaths.
func dirWritable(dir string) error {
	return unix.Access(dir, unix.W_OK)
}
