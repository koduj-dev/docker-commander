// Package service implements `dockercmd --install-service` and friends: it
// installs Docker Commander as a background service for the current OS so the
// monitor, alert engine and metric history keep running without a browser.
//
// Linux installs the hardened systemd unit (the same one in deploy/, embedded
// here and kept identical by a test); macOS installs a per-user launchd
// LaunchAgent. Windows is not supported here yet — use deploy/install-windows.ps1.
//
// The OS-specific Install/Uninstall/Status live in service_{linux,darwin}.go;
// this file holds the embedded templates and small shared helpers.
package service

import (
	_ "embed"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemdUnit is byte-identical to deploy/dockercmd.service (enforced by
// TestSystemdUnitMatchesDeployFile) so the embedded and reference units never
// drift apart.
//
//go:embed unit.service
var systemdUnit string

// launchdPlistTmpl is the per-user LaunchAgent plist, rendered with the paths
// computed at install time (text/template; see service_darwin.go).
//
//go:embed agent.plist.tmpl
var launchdPlistTmpl string

const (
	// binDest is where the installer places the binary so the service points at
	// a stable path that survives moving/`--self-upgrade` of the source binary.
	binDest = "/usr/local/bin/dockercmd"

	svcUser      = "dockercmd"
	linuxDataDir = "/var/lib/dockercmd"
	unitPath     = "/etc/systemd/system/dockercmd.service"
	launchdLabel = "dev.koduj.dockercmd"
)

// copyFile copies src to dst (creating parent dirs) via a temp file + rename, so
// replacing a running executable doesn't fail with ETXTBSY mid-write.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp-install"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// runCmd runs an external command, streaming its output to w.
func runCmd(w io.Writer, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = w
	c.Stderr = w
	return c.Run()
}

// xmlEscape escapes a string for safe interpolation into the launchd plist
// (XML). The values are our own paths, not untrusted input, so this is defensive
// correctness — a home dir with an '&' would otherwise produce an invalid plist
// that launchctl refuses to load.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// selfPath returns the absolute, symlink-resolved path of the running binary.
func selfPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self, nil
}
