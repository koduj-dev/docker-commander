#!/bin/sh
# Mirrors `dockercmd --install-service`: a dedicated unprivileged user in the
# docker group, an owned data dir, a locked-down config, and the enabled+running
# service. Idempotent and safe on both install and upgrade (it restarts into the
# freshly-installed binary). Runs as root from dpkg/rpm.
set -e

if ! getent passwd dockercmd >/dev/null 2>&1; then
	useradd --system --no-create-home --shell /usr/sbin/nologin dockercmd
fi

# Membership of the docker group lets the service reach the daemon socket — which
# is effectively ROOT ON THE HOST. Anyone who can run as this user (or who
# compromises the web UI) can control Docker. This mirrors the systemd setup;
# secure the UI accordingly (localhost / HTTPS + strong auth).
if getent group docker >/dev/null 2>&1; then
	usermod -aG docker dockercmd || true
	echo "dockercmd: added 'dockercmd' to the 'docker' group — host-root-equivalent; keep the UI locked down." >&2
fi

install -d -o dockercmd -g dockercmd -m 0750 /var/lib/dockercmd

# The config can hold secrets (DC_REDIS_PASSWORD, DC_METRICS_TOKEN), so keep it
# readable only by root and the service group — never world-readable.
if [ -f /etc/docker-commander/commander.conf ]; then
	chgrp dockercmd /etc/docker-commander/commander.conf 2>/dev/null || true
	chmod 0640 /etc/docker-commander/commander.conf || true
fi

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	systemctl enable dockercmd.service || true
	# `restart` (not `start`) so an upgrade actually runs the new binary; on a
	# fresh install it just starts it. Harmless if it can't come up yet (e.g.
	# Docker not installed) — start it later with `systemctl start dockercmd`.
	systemctl restart dockercmd.service || true
fi

exit 0
