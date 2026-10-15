#!/bin/sh
# Print one version's section of CHANGELOG.md — the release notes for
# tag v<version> (ADR 0011). Exits 1 (printing nothing) when the section
# is missing, so a release without notes fails instead of shipping blank.
#
# Usage: scripts/changelog-section.sh 1.3.0   (a leading v is accepted)
set -eu

version="${1#v}"
changelog="$(dirname "$0")/../CHANGELOG.md"

section="$(awk -v want="${version}" '
	/^## \[/ {
		if (found) exit
		v = $0
		sub(/^## \[v?/, "", v)
		sub(/\].*$/, "", v)
		if (v == want) { found = 1; next }
	}
	found { print }
	END { if (!found) exit 1 }
' "${changelog}")" || {
	echo "CHANGELOG.md has no '## [${version}]' section" >&2
	exit 1
}
# $(...) already dropped the trailing blank lines; drop the leading ones.
printf '%s\n' "${section}" | sed -e '/./,$!d'
