//go:build linux

package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
)

// Install sets Docker Commander up as a systemd service: it copies the binary to
// a stable path, creates a dedicated unprivileged user in the docker group,
// writes the hardened unit, and enables + starts it. Requires root.
func Install(w io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("installing the systemd service needs root — re-run with sudo")
	}

	// 1. Copy ourselves to a stable location the unit can point at.
	self, err := selfPath()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	if self != binDest {
		if err := copyFile(self, binDest, 0o755); err != nil {
			return fmt.Errorf("install binary to %s: %w", binDest, err)
		}
		fmt.Fprintf(w, "Installed binary → %s\n", binDest)
	}

	// 2. Dedicated unprivileged user, in the docker group so it reaches the socket.
	if _, err := user.Lookup(svcUser); err != nil {
		if err := runCmd(w, "useradd", "--system", "--no-create-home",
			"--shell", "/usr/sbin/nologin", svcUser); err != nil {
			return fmt.Errorf("create system user %q: %w", svcUser, err)
		}
		fmt.Fprintf(w, "Created system user %q\n", svcUser)
	}
	if _, err := user.LookupGroup("docker"); err == nil {
		if err := runCmd(w, "usermod", "-aG", "docker", svcUser); err != nil {
			return fmt.Errorf("add %q to the docker group: %w", svcUser, err)
		}
	} else {
		fmt.Fprintf(w, "warning: no 'docker' group found — is Docker installed? Add it later:\n"+
			"         sudo usermod -aG docker %s\n", svcUser)
	}

	// 3. Data dir, owned by the service user.
	if err := os.MkdirAll(linuxDataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	// Hand the data dir to the service user — without this it stays root-owned
	// and the unprivileged service can't write its DB/keys. A lookup miss here is
	// fatal, not skippable (we just created the user).
	u, err := user.Lookup(svcUser)
	if err != nil {
		return fmt.Errorf("look up %q to own the data dir: %w", svcUser, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.Chown(linuxDataDir, uid, gid); err != nil {
		return fmt.Errorf("chown data dir: %w", err)
	}

	// 4. Hardened unit (identical to deploy/dockercmd.service).
	if err := os.WriteFile(unitPath, []byte(systemdUnit), 0o644); err != nil {
		return fmt.Errorf("write unit %s: %w", unitPath, err)
	}
	fmt.Fprintf(w, "Wrote unit → %s\n", unitPath)

	// 5. Reload + enable + start.
	if err := runCmd(w, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := runCmd(w, "systemctl", "enable", "--now", "dockercmd"); err != nil {
		return fmt.Errorf("systemctl enable --now dockercmd: %w", err)
	}

	fmt.Fprintln(w, "\n✅ Service installed and started.")
	fmt.Fprintln(w, "   Status: dockercmd --service-status   (logs: journalctl -u dockercmd -f)")
	fmt.Fprintln(w, "   Listen address + TLS come from your config (DC_HOST/DC_PORT/DC_TLS_*),")
	fmt.Fprintln(w, "   e.g. /etc/docker-commander/commander.conf. Then create the admin account in the UI.")
	return nil
}

// Uninstall stops and removes the systemd service. The data dir and the service
// user are left in place so reinstalling keeps the database and keys.
func Uninstall(w io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("uninstalling the systemd service needs root — re-run with sudo")
	}
	if err := runCmd(w, "systemctl", "disable", "--now", "dockercmd"); err != nil {
		fmt.Fprintf(w, "note: systemctl disable returned: %v (continuing)\n", err)
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit %s: %w", unitPath, err)
	}
	if err := runCmd(w, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	fmt.Fprintf(w, "\nRemoved %s.\n", unitPath)
	fmt.Fprintf(w, "Left in place: data dir %s and user %q (delete them by hand to purge).\n", linuxDataDir, svcUser)
	return nil
}

// Status prints `systemctl status` for the service.
func Status(w io.Writer) error {
	// systemctl status exits non-zero when the unit is inactive/absent; that's
	// informational here, so the exit code is intentionally ignored.
	_ = runCmd(w, "systemctl", "status", "dockercmd", "--no-pager")
	return nil
}
