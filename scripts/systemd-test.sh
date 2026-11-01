#!/bin/sh
# Installs, upgrades, removes and purges the deb package under a running
# systemd, and checks what wegweiser's D54 promises at each step.
#
#   make package VERSION=0.0.1 && make package VERSION=0.0.2
#   sudo scripts/systemd-test.sh dist/wegwitness_0.0.1_amd64.deb dist/wegwitness_0.0.2_amd64.deb
#
# The two packages must be different versions, the first older. Needs root
# and a running systemd, which CI's runner has. It refuses to run on a machine
# that already has a witness on it, because cleaning up afterwards means
# deleting the state directory, and on such a machine that is a cluster's log.
set -eu

old=$1
new=$2
config=/etc/wegwitness/config.yaml
unit=/usr/lib/systemd/system/wegwitness.service
state=/var/lib/private/wegwitness

for path in /usr/bin/wegwitness /etc/wegwitness /etc/systemd/system/wegwitness.service \
	"$unit" /var/lib/wegwitness "$state"; do
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
	dpkg --purge wegwitness >/dev/null 2>&1 || true
	systemctl reset-failed wegwitness 2>/dev/null || true
	rm -rf /etc/wegwitness /var/lib/wegwitness "$state"
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

pid() { systemctl show -p MainPID --value wegwitness; }

sum() { sha256sum "$1" | cut -d' ' -f1; }

echo "--- a configuration already there survives the first installation"
install -d -m 0755 /etc/wegwitness
secret=$(head -c 32 /dev/urandom | base64)
(umask 077 && printf '%s\n' 'cluster:' '  listen: "127.0.0.1:18054"' \
	'  advertise: "127.0.0.1:18054"' "  secret: \"$secret\"" >"$config")
before=$(sum "$config")
dpkg -i "$old" >/dev/null
[ "$(sum "$config")" = "$before" ] || fail "the installation changed $config"
echo ok

echo "--- installing starts nothing"
[ -f "$unit" ] || fail "no unit at $unit"
[ "$(systemctl is-active wegwitness || true)" = inactive ] || fail "the witness is running"
[ "$(systemctl is-enabled wegwitness || true)" = disabled ] || fail "the witness is enabled"
echo ok

echo "--- the unit starts it, with a configuration only root can read"
systemctl enable --now wegwitness
answering || fail "wegwitness health did not answer within 20 seconds"
echo ok

echo "--- an upgrade restarts a running witness"
was=$(pid)
dpkg -i "$new" >/dev/null
answering || fail "wegwitness health did not answer after the upgrade"
[ "$(pid)" != "$was" ] || fail "the witness was not restarted"
[ "$(sum "$config")" = "$before" ] || fail "the upgrade changed $config"
echo ok

echo "--- an upgrade leaves a stopped witness stopped"
systemctl stop wegwitness
dpkg -i "$new" >/dev/null
[ "$(systemctl is-active wegwitness || true)" = inactive ] || fail "the reinstallation started the witness"
echo ok

echo "--- a missing configuration stops the start"
mv "$config" "$config.away"
if systemctl start wegwitness 2>/dev/null; then
	fail "the unit started without a configuration file"
fi
mv "$config.away" "$config"
echo ok

echo "--- a witness that does not come back after an upgrade leaves dpkg working"
systemctl start wegwitness
answering || fail "wegwitness health did not answer"
mv "$config" "$config.away"
dpkg -i "$new" >/tmp/systemd-test-dpkg.log 2>&1 || {
	cat /tmp/systemd-test-dpkg.log >&2
	fail "dpkg failed because the restart did"
}
dpkg-query -W -f '${Status}' wegwitness | grep -q 'install ok installed' ||
	fail "the package is $(dpkg-query -W -f '${Status}' wegwitness)"
mv "$config.away" "$config"
echo ok

echo "--- removing keeps the configuration and the log"
dpkg -r wegwitness >/dev/null
[ ! -e "$unit" ] || fail "the unit is still there"
[ -f "$config" ] || fail "removing took $config"
[ -n "$(ls -A "$state")" ] || fail "removing took the log"
echo ok

echo "--- purging takes the configuration and keeps the log"
dpkg -i "$new" >/dev/null
dpkg --purge wegwitness >/dev/null
[ ! -e /etc/wegwitness ] || fail "purging left /etc/wegwitness"
[ -n "$(ls -A "$state")" ] || fail "purging took the log"
echo ok

echo "--- a first installation writes the example as the configuration, mode 0600"
dpkg -i "$new" >/dev/null
[ "$(sum "$config")" = "$(sum /usr/share/doc/wegwitness/wegwitness.example.yaml)" ] ||
	fail "$config is not the example"
[ "$(stat -c '%a %U' "$config")" = "600 root" ] || fail "$config is $(stat -c '%a %U' "$config")"
echo ok

echo "--- exposure"
score=$(systemd-analyze security --offline=true "$unit" | tail -1 |
	awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+\.[0-9]+$/) { print $i; exit } }')
echo "systemd-analyze security: $score"
awk -v s="$score" 'BEGIN { exit !(s != "" && s <= 1.1) }' ||
	fail "the unit scores $score, above the 1.1 it had"
echo ok
