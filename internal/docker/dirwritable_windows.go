//go:build windows

package docker

// dirWritable has no cheap equivalent on Windows, where ACLs decide; a failed
// save there reports the real error instead.
func dirWritable(string) error { return nil }
