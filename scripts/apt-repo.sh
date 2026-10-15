#!/usr/bin/env bash
# Add packages to the syncphony-box apt repository and re-sign it
# (MAD-810, ADR 0012). The release job runs this on a checkout of the
# gh-pages branch; GitHub Pages serves the result at
# https://madeofpendletonwool.github.io/syncphony-box/apt.
#
# Usage: scripts/apt-repo.sh REPO_DIR DEB...
#
# Environment:
#   APT_SIGNING_KEY_ID   the gpg key (already imported) that signs Release
#   KEEP                 versions kept per architecture (default 5; older
#                        ones are pruned — Pages is no archive, the
#                        GitHub Releases are)
#
# Layout (one suite, "stable", one component, "main"):
#   pool/main/s/syncphony-box/syncphony-box_<version>_<arch>.deb
#   dists/stable/main/binary-<arch>/Packages{,.gz}
#   dists/stable/{Release,Release.gpg,InRelease}
#
# Needs apt-ftparchive (apt-utils), dpkg-deb and gpg.
set -euo pipefail

if [ "$#" -lt 2 ]; then
	echo "usage: $0 REPO_DIR DEB..." >&2
	exit 2
fi
: "${APT_SIGNING_KEY_ID:?set APT_SIGNING_KEY_ID to the signing key}"
KEEP="${KEEP:-5}"
REPO="$1"
shift

POOL="pool/main/s/syncphony-box"
mkdir -p "${REPO}/${POOL}"
for deb in "$@"; do
	pkg="$(dpkg-deb -f "${deb}" Package)"
	if [ "${pkg}" != "syncphony-box" ]; then
		echo "refusing ${deb}: package ${pkg}, not syncphony-box" >&2
		exit 1
	fi
	cp -v "${deb}" "${REPO}/${POOL}/"
done

cd "${REPO}"

# Prune to the newest KEEP versions per architecture.
archs="$(for f in "${POOL}"/*.deb; do dpkg-deb -f "${f}" Architecture; done | sort -u)"
for arch in ${archs}; do
	mapfile -t old < <(
		for f in "${POOL}"/*_"${arch}".deb; do
			printf '%s %s\n' "$(dpkg-deb -f "${f}" Version)" "${f}"
		done | sort -V | head -n "-${KEEP}" | cut -d' ' -f2-
	)
	for f in "${old[@]}"; do
		echo "pruning ${f}"
		rm -f "${f}"
	done
done

rm -rf dists/stable
for arch in ${archs}; do
	dir="dists/stable/main/binary-${arch}"
	mkdir -p "${dir}"
	apt-ftparchive --arch "${arch}" packages "${POOL}" > "${dir}/Packages"
	gzip -9kn "${dir}/Packages"
done

apt-ftparchive \
	-o APT::FTPArchive::Release::Origin="Syncphony Box" \
	-o APT::FTPArchive::Release::Label="Syncphony Box" \
	-o APT::FTPArchive::Release::Suite=stable \
	-o APT::FTPArchive::Release::Codename=stable \
	-o APT::FTPArchive::Release::Components=main \
	-o APT::FTPArchive::Release::Architectures="$(echo ${archs})" \
	-o APT::FTPArchive::Release::Description="The Syncphony box's own software" \
	release dists/stable > dists/stable/Release.tmp
mv dists/stable/Release.tmp dists/stable/Release

gpg --batch --yes --local-user "${APT_SIGNING_KEY_ID}" --clearsign \
	-o dists/stable/InRelease dists/stable/Release
gpg --batch --yes --local-user "${APT_SIGNING_KEY_ID}" --armor --detach-sign \
	-o dists/stable/Release.gpg dists/stable/Release

echo "apt repo updated: ${archs} — $(find "${POOL}" -name '*.deb' | wc -l) package(s)"
