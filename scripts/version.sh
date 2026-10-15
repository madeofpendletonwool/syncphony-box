#!/bin/sh
# The box software's version, from git. One definition for the image name,
# boxd's -X main.version and the .deb (ADR 0011).
#
#   scripts/version.sh          v1.3.0, v1.3.0-4-gabc1234, v1.3.0-dirty, abc1234
#   scripts/version.sh --deb    1.3.0, 1.3.0+git4.gabc1234, 1.3.0+dirty, 0.0.0+gabc1234
#
# The Debian form sorts the way the releases do: a build past a tag is
# newer than the tag and older than the next one, so a box never
# "updates" from a dev build back to the release it was cut after.
set -eu

cd "$(dirname "$0")/.."
v="$(git describe --tags --always --dirty 2>/dev/null || echo unversioned)"

if [ "${1:-}" != "--deb" ]; then
	echo "${v}"
	exit 0
fi

dirty=""
case "${v}" in
*-dirty) v="${v%-dirty}" dirty=1 ;;
esac

case "${v}" in
v[0-9]*)
	v="${v#v}"
	# 1.3.0-4-gabc1234 -> 1.3.0+git4.gabc1234
	deb="$(printf '%s' "${v}" | sed -E 's/-([0-9]+)-(g[0-9a-f]+)$/+git\1.\2/')"
	;;
unversioned) deb="0.0.0" ;;
*) deb="0.0.0+g${v}" ;; # no tag yet: just a commit
esac

if [ -n "${dirty}" ]; then
	case "${deb}" in
	*+*) deb="${deb}.dirty" ;;
	*) deb="${deb}+dirty" ;;
	esac
fi
echo "${deb}"
