#!/bin/sh
# Runs before the package's files go. dpkg passes "remove" when the package is
# going and "upgrade" when it is being replaced; rpm passes 0 and 1 for the
# same two. Only a removal stops the witness: an upgrade restarts it afterwards.
set -e

case "$1" in
remove | 0)
	# A unit that will not stop must not keep the package from going.
	if [ -d /run/systemd/system ]; then
		systemctl disable --now wegwitness.service || true
	fi
	;;
esac
