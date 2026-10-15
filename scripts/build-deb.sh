#!/usr/bin/env bash
# Build the syncphony-box .deb (MAD-810, ADR 0012): boxd plus every file
# the box's own software is made of — the tree under packaging/root —
# with the maintainer scripts in packaging/debian. The image installs this
# package instead of copying files, and boxes update to newer ones from
# the apt repo, so the image and the updates can't drift apart.
#
# Usage: scripts/build-deb.sh ARCH BOXD_BINARY OUT_DIR
#   ARCH          Debian architecture: arm64 (Pi) or amd64 (x86, MAD-846)
#   BOXD_BINARY   a static boxd for that architecture
#   OUT_DIR       where syncphony-box_<version>_<arch>.deb lands
#
# Environment:
#   DEB_VERSION   override the version (default: scripts/version.sh --deb)
#   APT_URL       the apt repository the package points boxes at
#                 (default: this repo's GitHub Pages)
#
# The apt source and its keyring are only packaged when the public key
# (packaging/apt/syncphony-box-archive-keyring.asc) is in the checkout. A
# box built without it never updates itself; the release job refuses to
# publish one.
set -euo pipefail

if [ "$#" -ne 3 ]; then
	echo "usage: $0 ARCH BOXD_BINARY OUT_DIR" >&2
	exit 2
fi
ARCH="$1" BOXD="$2" OUT="$3"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${DEB_VERSION:-$("${REPO_ROOT}/scripts/version.sh" --deb)}"
APT_URL="${APT_URL:-https://madeofpendletonwool.github.io/syncphony-box/apt}"
KEYRING="${REPO_ROOT}/packaging/apt/syncphony-box-archive-keyring.asc"

if ! command -v dpkg-deb >/dev/null 2>&1; then
	echo "dpkg-deb is required to build the package (apt install dpkg)." >&2
	exit 1
fi
if [ ! -f "${BOXD}" ]; then
	echo "boxd binary not found: ${BOXD}" >&2
	exit 1
fi

STAGING="$(mktemp -d)"
trap 'rm -rf "${STAGING}"' EXIT
PKG="${STAGING}/pkg"

cp -a "${REPO_ROOT}/packaging/root" "${PKG}"
install -m 0755 "${BOXD}" "${PKG}/usr/lib/syncphony-box/boxd"

if [ -f "${KEYRING}" ]; then
	install -d "${PKG}/usr/share/keyrings" "${PKG}/usr/share/syncphony-box/apt"
	install -m 0644 "${KEYRING}" "${PKG}/usr/share/keyrings/syncphony-box-archive-keyring.asc"
	sed "s|@APT_URL@|${APT_URL}|" "${REPO_ROOT}/packaging/apt/syncphony-box.list" \
		> "${PKG}/usr/share/syncphony-box/apt/syncphony-box.list"
else
	echo "WARNING: ${KEYRING#"${REPO_ROOT}"/} is missing: this package has no apt source," >&2
	echo "WARNING: so a box built from it never updates itself (see docs/releasing.md)." >&2
fi

# Git doesn't keep modes beyond the executable bit, and the checkout's
# umask decides the rest: normalize, then mark the scripts executable.
find "${PKG}" -type d -exec chmod 0755 {} +
find "${PKG}" -type f -exec chmod 0644 {} +
chmod 0755 "${PKG}/usr/bin/syncphony-box" \
	"${PKG}/usr/lib/syncphony-box/boxd" \
	"${PKG}/usr/lib/syncphony-box/kiosk" \
	"${PKG}/usr/lib/syncphony-box/audio-setup" \
	"${PKG}/usr/lib/syncphony-box/data-persist" \
	"${PKG}/etc/initramfs-tools/scripts/local-premount/grow-data" \
	"${PKG}/etc/initramfs-tools/hooks/syncphony-box"

install -d -m 0755 "${PKG}/DEBIAN"
installed_kb="$(du -sk --apparent-size "${PKG}" | cut -f1)"
sed -e "s|@VERSION@|${VERSION}|" -e "s|@ARCH@|${ARCH}|" -e "s|@INSTALLED_SIZE@|${installed_kb}|" \
	"${REPO_ROOT}/packaging/debian/control.in" > "${PKG}/DEBIAN/control"
install -m 0755 "${REPO_ROOT}/packaging/debian/postinst" "${PKG}/DEBIAN/postinst"
install -m 0644 "${REPO_ROOT}/packaging/debian/triggers" "${PKG}/DEBIAN/triggers"
# Every file under /etc is a conffile (dpkg-deb doesn't mark them itself).
(cd "${PKG}" && find etc -type f | sort | sed 's|^|/|') > "${PKG}/DEBIAN/conffiles"
chmod 0644 "${PKG}/DEBIAN/conffiles" "${PKG}/DEBIAN/control"

mkdir -p "${OUT}"
DEB="${OUT}/syncphony-box_${VERSION}_${ARCH}.deb"
dpkg-deb --root-owner-group -Zxz --build "${PKG}" "${DEB}" >/dev/null
echo "${DEB}"
