//go:build !windows

package config

// secureDataDir is a no-op on Unix: MkdirAll's 0o700 mode already restricts
// the directory to its owner, which is real enforcement there (unlike on
// Windows, where Unix mode bits don't translate to an NTFS ACL at all). A var
// (not a plain func), matching securedatadir_windows.go, so Load's own test
// can override it on any platform to prove Load actually calls and checks it
// — the real Windows ACL logic itself is only exercised on windows-latest CI
// (see .github/workflows/windows-service-smoke.yml), but the wiring that
// calls it is platform-independent and worth proving here.
var secureDataDir = func(dir string) error { return nil }
