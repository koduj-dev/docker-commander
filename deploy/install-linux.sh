#!/usr/bin/env bash
#
# Install Docker Commander as a systemd service on Linux.
#
# Idempotent: safe to re-run to upgrade the binary or refresh the unit.
#
#   sudo ./deploy/install-linux.sh [path-to-dockercmd-binary]
#
# If no binary path is given it looks for ./dockercmd, then a
# dockercmd-linux-<arch> in the current dir (a downloaded release).
#
set -euo pipefail

BIN_DST=/usr/local/bin/dockercmd
CONF_DIR=/etc/docker-commander
CONF=$CONF_DIR/commander.conf
DATA_DIR=/var/lib/dockercmd
UNIT=/etc/systemd/system/dockercmd.service
SVC_USER=dockercmd

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

if [[ $EUID -ne 0 ]]; then
  echo "Run as root:  sudo $0" >&2
  exit 1
fi

# --- locate the binary -------------------------------------------------------
arch=$(uname -m); case "$arch" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; esac
BIN_SRC=${1:-}
if [[ -z "$BIN_SRC" ]]; then
  for cand in ./dockercmd "./dockercmd-linux-$arch"; do
    [[ -f "$cand" ]] && { BIN_SRC=$cand; break; }
  done
fi
if [[ -z "$BIN_SRC" || ! -f "$BIN_SRC" ]]; then
  echo "Binary not found. Build it (make build) or download a release, then:" >&2
  echo "  sudo $0 /path/to/dockercmd" >&2
  exit 1
fi
echo "==> Binary: $BIN_SRC"

# --- dedicated unprivileged user, in the docker group -----------------------
if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  echo "==> Creating system user '$SVC_USER'"
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
fi
if getent group docker >/dev/null 2>&1; then
  usermod -aG docker "$SVC_USER"
else
  echo "!! No 'docker' group found — is Docker installed? The service needs it" >&2
  echo "   to reach the daemon socket. Add it later: usermod -aG docker $SVC_USER" >&2
fi

# --- install binary, config, data dir ---------------------------------------
echo "==> Installing $BIN_DST"
install -m755 "$BIN_SRC" "$BIN_DST"

install -d "$CONF_DIR"
if [[ ! -f "$CONF" ]]; then
  echo "==> Installing default config $CONF (edit it later)"
  # root-owned but group-readable by the service user: it must read this file,
  # yet stays unwritable by the (unprivileged) service and unreadable by others
  # — matters once you put secrets (DC_REDIS_PASSWORD, …) in it.
  install -m640 -g "$SVC_USER" "$SCRIPT_DIR/commander.conf.example" "$CONF"
else
  echo "==> Keeping existing $CONF"
fi

install -d -o "$SVC_USER" -g "$SVC_USER" -m750 "$DATA_DIR"

# --- systemd unit ------------------------------------------------------------
echo "==> Installing unit $UNIT"
install -m644 "$SCRIPT_DIR/dockercmd.service" "$UNIT"

# --- man page ----------------------------------------------------------------
MAN_DIR=/usr/local/share/man/man1
if [[ -f "$SCRIPT_DIR/dockercmd.1" ]]; then
  echo "==> Installing man page $MAN_DIR/dockercmd.1  (man dockercmd)"
  install -d "$MAN_DIR"
  install -m644 "$SCRIPT_DIR/dockercmd.1" "$MAN_DIR/dockercmd.1"
fi

echo "==> daemon-reload + enable --now"
systemctl daemon-reload
systemctl enable --now dockercmd

sleep 2
systemctl --no-pager --full status dockercmd | head -n 12 || true

echo ""
echo "✅ Done. The service is installed and running."
echo "   Status:  systemctl status dockercmd   (logs: journalctl -u dockercmd -f)"
echo "   Config:  $CONF   (then: sudo systemctl restart dockercmd)"
echo "   Listen address + TLS come from that config (DC_HOST/DC_PORT/DC_TLS_*);"
echo "   create the admin account in the UI on first visit."
