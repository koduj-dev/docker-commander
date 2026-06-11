#!/usr/bin/env bash
#
# Install Docker Commander as a launchd service on macOS.
#
# Installs a per-user LaunchAgent (not a system LaunchDaemon) on purpose: with
# Docker Desktop the daemon socket is owned by the logged-in user, so a system
# daemon running as root/_dockercmd usually can't reach it. The agent runs as
# you, starts at login, and is restarted automatically (KeepAlive).
#
# Idempotent: re-run to upgrade the binary or reload the agent.
#
#   ./deploy/install-macos.sh [path-to-dockercmd-binary]
#
# If no path is given it looks for ./dockercmd, then dockercmd-darwin-<arch>.
#
set -euo pipefail

LABEL=dev.koduj.dockercmd
BIN_DST=/usr/local/bin/dockercmd
DATA_DIR="$HOME/Library/Application Support/dockercmd"
LOG_DIR="$HOME/Library/Logs"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

if [[ $EUID -eq 0 ]]; then
  echo "Run as your normal user (NOT sudo) — the agent must reach your Docker socket." >&2
  exit 1
fi

# --- locate the binary -------------------------------------------------------
arch=$(uname -m); case "$arch" in x86_64) arch=amd64;; arm64) arch=arm64;; esac
BIN_SRC=${1:-}
if [[ -z "$BIN_SRC" ]]; then
  for cand in ./dockercmd "./dockercmd-darwin-$arch"; do
    [[ -f "$cand" ]] && { BIN_SRC=$cand; break; }
  done
fi
if [[ -z "$BIN_SRC" || ! -f "$BIN_SRC" ]]; then
  echo "Binary not found. Build it (make build) or download a release, then:" >&2
  echo "  $0 /path/to/dockercmd" >&2
  exit 1
fi
echo "==> Binary: $BIN_SRC"

# --- install binary + data dir ----------------------------------------------
echo "==> Installing $BIN_DST (may prompt for sudo to write /usr/local/bin)"
sudo install -m755 "$BIN_SRC" "$BIN_DST"
mkdir -p "$DATA_DIR" "$(dirname "$PLIST")"

# --- write the LaunchAgent plist --------------------------------------------
echo "==> Writing $PLIST"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN_DST</string>
    <string>-data-dir</string>
    <string>$DATA_DIR</string>
  </array>
  <key>RunAtLoad</key>        <true/>
  <key>KeepAlive</key>        <true/>
  <key>StandardOutPath</key>  <string>$LOG_DIR/dockercmd.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/dockercmd.log</string>
  <!-- Help the binary find the Docker Desktop CLI + socket. -->
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  </dict>
</dict>
</plist>
EOF

# --- (re)load the agent ------------------------------------------------------
echo "==> Loading the agent"
launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$UID" "$PLIST"
launchctl enable "gui/$UID/$LABEL" 2>/dev/null || true

sleep 2
if launchctl print "gui/$UID/$LABEL" >/dev/null 2>&1; then
  echo ""
  echo "✅ Done. The agent is installed and running."
  echo "   Logs:   tail -f \"$LOG_DIR/dockercmd.log\""
  echo "   Stop:   launchctl bootout gui/\$UID/$LABEL"
  echo "   Start:  launchctl bootstrap gui/\$UID \"$PLIST\""
  echo "   Listen address + TLS come from DC_HOST/DC_PORT/DC_TLS_* (default 127.0.0.1:8470);"
  echo "   create the admin account in the UI on first visit."
else
  echo "!! Agent did not come up — check $LOG_DIR/dockercmd.log" >&2
  exit 1
fi
