//go:build windows

package selfupdate

// dirWritable has no cheap equivalent on Windows, where ACLs decide. The
// in-app update isn't offered there anyway, and the install itself still
// runs preflightWritable before downloading.
func dirWritable(string) error { return nil }
