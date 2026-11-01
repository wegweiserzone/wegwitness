#!/bin/sh
# Installs the deb where dpkg drops everything under /usr/share/doc but the
# copyright files, as Ubuntu's container and cloud images do, and checks that
# the configuration is still written and the licence still travels.
#
#   scripts/deb-minimal-test.sh dist/wegweiser_0.0.2_amd64.deb    # in ubuntu:24.04
set -eu

name=$(dpkg-deb -f "$1" Package)
grep -q '^path-exclude=/usr/share/doc/\*' /etc/dpkg/dpkg.cfg.d/* ||
	{ echo "FAIL: this dpkg keeps /usr/share/doc, so the test proves nothing" >&2; exit 1; }

dpkg -i "$1"
[ "$(stat -c '%a %U' "/etc/$name/config.yaml")" = "600 root" ] ||
	{ echo "FAIL: no configuration of mode 0600 at /etc/$name/config.yaml" >&2; exit 1; }
[ -s "/usr/share/doc/$name/copyright" ] ||
	{ echo "FAIL: the licence is not at /usr/share/doc/$name/copyright" >&2; exit 1; }
echo ok
