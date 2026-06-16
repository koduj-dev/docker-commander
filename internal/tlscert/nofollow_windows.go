//go:build windows

package tlscert

// Windows has no O_NOFOLLOW; the flag is a no-op there. Symlink semantics differ
// and the data directory is expected to be ACL-restricted to the service user.
const oNoFollow = 0
