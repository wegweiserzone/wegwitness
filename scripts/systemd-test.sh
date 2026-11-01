#!/bin/sh
# Installs wegwitness by hand, under a running systemd, and checks that the
# unit starts it with a configuration only root can read.
#
#   sudo scripts/systemd-test.sh [path/to/wegwitness]
#
# Needs root and a running systemd, which CI's runner has. It refuses to run
# on a machine that already has a witness on it, because cleaning up
# afterwards means deleting the state directory, and on such a machine that
# is a cluster's log.
set -eu

bin=${1:-bin/wegwitness}
unit=packaging/systemd/wegwitness.service
config=/etc/wegwitness/config.yaml

for path in /usr/bin/wegwitness /etc/wegwitness /etc/systemd/system/wegwitness.service \
	/var/lib/wegwitness /var/lib/private/wegwitness; do
	if [ -e "$path" ]; then
		echo "systemd-test: $path exists; this is for a machine without a witness on it" >&2
		exit 2
	fi
done

fail() {
	echo "FAIL: $*" >&2
	journalctl -u wegwitness --no-pager -n 30 >&2 || true
	exit 1
}

cleanup() {
	systemctl stop wegwitness 2>/dev/null || true
	systemctl reset-failed wegwitness 2>/dev/null || true
	rm -rf /usr/bin/wegwitness /etc/systemd/system/wegwitness.service /etc/wegwitness \
		/var/lib/wegwitness /var/lib/private/wegwitness
	systemctl daemon-reload
}
trap cleanup EXIT

# A witness in no cluster answers its own health check with exactly that,
# which it can only do once it has read its configuration and is listening.
answering() {
	i=0
	while [ "$i" -lt 20 ]; do
		if wegwitness health 2>&1 | grep -q 'in no cluster yet'; then
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	return 1
}

install -m 0755 "$bin" /usr/bin/wegwitness
install -m 0644 "$unit" /etc/systemd/system/wegwitness.service
install -d -m 0755 /etc/wegwitness
secret=$(head -c 32 /dev/urandom | base64)
(umask 077 && printf '%s\n' 'cluster:' '  listen: "127.0.0.1:18054"' \
	'  advertise: "127.0.0.1:18054"' "  secret: \"$secret\"" >"$config")
systemctl daemon-reload

echo "--- a configuration of mode 0600 is read"
systemctl start wegwitness || fail "the unit did not start"
answering || fail "wegwitness health did not answer within 20 seconds"
echo ok

echo "--- a missing configuration stops the start"
systemctl stop wegwitness
mv "$config" "$config.away"
if systemctl start wegwitness 2>/dev/null; then
	fail "the unit started without a configuration file"
fi
mv "$config.away" "$config"
echo ok

echo "--- exposure"
score=$(systemd-analyze security --offline=true "$unit" | tail -1 |
	awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+\.[0-9]+$/) { print $i; exit } }')
echo "systemd-analyze security: $score"
awk -v s="$score" 'BEGIN { exit !(s != "" && s <= 1.1) }' ||
	fail "the unit scores $score, above the 1.1 it had"
echo ok
