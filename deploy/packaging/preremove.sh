#!/bin/sh
# Stop + disable the service before the files go away.
set -e

if [ -d /run/systemd/system ]; then
	systemctl stop dockercmd.service || true
	systemctl disable dockercmd.service || true
fi

exit 0
