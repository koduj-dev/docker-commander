//go:build !windows

package docker

import "syscall"

// diskFree reports the total and free bytes on the filesystem containing
// path, using the platform's native statfs call.
func diskFree(path string) (totalBytes, freeBytes uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	// Bsize is int64 on some platforms (darwin/arm64), uint32/int32 on
	// others (linux) — the uint64 conversion is what makes this one
	// implementation work across all of them without a second build tag.
	bsize := uint64(stat.Bsize)
	return stat.Blocks * bsize, stat.Bavail * bsize, nil
}
