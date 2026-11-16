//go:build !windows

package docker

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// writeCheckSupported reports whether pathWritable gives a real answer here.
const writeCheckSupported = true

// dirWritable reports whether this process may create files in dir. Creating
// (or renaming over) an entry needs search permission on the directory as well
// as write, so both are asked. access(2) answers for the real uid and also
// returns EROFS on a read-only mount, which is what systemd's
// ProtectSystem=strict makes of the tree outside ReadWritePaths.
func dirWritable(dir string) error {
	return unix.Access(dir, unix.W_OK|unix.X_OK)
}

// pathWritable is dirWritable for a directory and plain write access for a file.
func pathWritable(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return dirWritable(p)
	}
	return unix.Access(p, unix.W_OK)
}

// openNoBlock opens p for reading without blocking: a FIFO swapped in for a
// compose file would otherwise make open(2) wait for a writer. The caller
// checks the descriptor's type afterwards.
func openNoBlock(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
