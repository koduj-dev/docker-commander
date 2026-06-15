#!/bin/sh
# Mirrors `dockercmd --install-service`: a dedicated unprivileged user in the
# docker group, an owned data dir, and the enabled service. Idempotent.
set -e

if ! getent passwd dockercmd >/dev/null 2>&1; then
	useradd --system --no-create-home --shell /usr/sbin/nologin dockercmd
fi

# Let the service reach the daemon socket (no-op if there's no docker group yet;
# `docker.io`/`docker-ce` is only Recommended, so it may be installed later).
if getent group docker >/dev/null 2>&1; then
	usermod -aG docker dockercmd || true
fi

install -d -o dockercmd -g dockercmd -m 0750 /var/lib/dockercmd

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	systemctl enable dockercmd.service || true
	# Start it; harmless to fail (e.g. Docker not installed yet) — the unit will
	# come up on the next boot / `systemctl start dockercmd` once Docker is there.
	systemctl start dockercmd.service || true
fi

exit 0
