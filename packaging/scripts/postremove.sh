#!/bin/sh
# Runs after the package's files have gone. dpkg passes "remove", "purge" or
# "upgrade"; rpm passes 0 on removal and 1 on upgrade, and has no purge.
#
# The log is never touched, purge included: it is the cluster's history, and
# no package manager gets to decide that it goes (wegweiser's
# docs/decisions/d54-installed-from-packages.md).
set -e

if [ "$1" = purge ]; then
	rm -f /etc/wegwitness/config.yaml
	rmdir /etc/wegwitness 2>/dev/null || true
fi

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
