#!/bin/sh
# Runs once the package's files are in place. dpkg passes "configure" and the
# version configured before, which is empty on a first installation; rpm passes
# 1 on a first installation and 2 or more on an upgrade.
#
# The witness is never started from here: its configuration needs the
# cluster's secret and an address first, and both are the administrator's to
# write (wegweiser's docs/decisions/d54-installed-from-packages.md).
set -e

case "$1" in
configure)
	if [ -n "${2:-}" ]; then upgrade=yes; else upgrade=no; fi
	;;
[0-9]*)
	if [ "$1" -ge 2 ]; then upgrade=yes; else upgrade=no; fi
	;;
*)
	exit 0
	;;
esac

config=/etc/wegwitness/config.yaml

# Written once, and never owned by the package. Mode 0600 because it holds the
# cluster's secret; the unit hands it to the witness as a credential.
if [ ! -e "$config" ]; then
	install -d -m 0755 /etc/wegwitness
	install -m 0600 -o root -g root /usr/share/wegwitness/wegwitness.example.yaml "$config"
fi

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	# A witness that was running runs the new binary; one that was stopped
	# stays stopped. One that does not come back is reported, not fatal: a
	# failing script would leave the package manager stuck for everything else
	# on the machine.
	if [ "$upgrade" = yes ] && ! systemctl try-restart wegwitness.service; then
		echo "wegwitness did not restart; journalctl -u wegwitness says why" >&2
	fi
fi

# A first installation, or one over a removal that left the unit disabled.
if ! { [ -d /run/systemd/system ] && systemctl is-enabled --quiet wegwitness.service; }; then
	echo "Edit $config, then start the witness: systemctl enable --now wegwitness"
fi
