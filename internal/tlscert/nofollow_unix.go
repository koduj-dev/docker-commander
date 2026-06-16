//go:build !windows

package tlscert

import "syscall"

// oNoFollow makes os.OpenFile refuse to follow a final-component symlink, so a
// pre-planted symlink at key.pem can't redirect the write onto another file.
const oNoFollow = syscall.O_NOFOLLOW
