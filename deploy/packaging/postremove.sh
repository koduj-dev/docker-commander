#!/bin/sh
# Reload systemd after the unit file is removed. The data dir (/var/lib/dockercmd)
# and the `dockercmd` user are deliberately left in place so reinstalling keeps
# the database and keys; remove them by hand to purge.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi

exit 0
