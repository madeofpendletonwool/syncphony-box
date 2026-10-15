#!/bin/bash
# Customise a Debian root filesystem into a Syncphony box (MAD-846, ADR
# 0013).
#
# This is the layer both image builders share. pi-gen (the Pi .img) and
# live-build (the x86 ISO) each run it inside their own chroot, as root,
# with the target root at /. It knows nothing about either builder: no
# on_chroot, no ROOTFS_DIR, no /boot/firmware. What's left per platform
# lives in the thin wrappers: the Pi's config.txt and cmdline.txt in
# stage-syncphony/, and the x86 bootloader and installer in x86/.
#
# Usage: customise.sh PLATFORM DEB
#   PLATFORM  pi or x86. Installs packages/common.list plus
#             packages/<PLATFORM>.list; the steps see it as SB_PLATFORM.
#   DEB       the syncphony-box .deb to install (scripts/build-deb.sh)
#
# Then runs steps/*.sh in order. Every step is idempotent.
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: $0 pi|x86 DEB" >&2
	exit 2
fi
SB_PLATFORM="$1"
DEB="$2"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export SB_PLATFORM DEBIAN_FRONTEND=noninteractive

case "${SB_PLATFORM}" in
pi | x86) ;;
*)
	echo "customise.sh: unknown platform '${SB_PLATFORM}' (want pi or x86)" >&2
	exit 2
	;;
esac
if [ ! -f "${DEB}" ]; then
	echo "customise.sh: ${DEB} is missing. Build it with scripts/build-deb.sh." >&2
	exit 1
fi

# Package lists: one name per line, # comments (pi-gen's format, and
# live-build's).
packages() {
	sed -e 's/#.*//' -e 's/[[:space:]]//g' -e '/^$/d' "$@"
}

# Builders leave the apt lists in place for their own installs; refresh
# only when there are none.
if ! compgen -G '/var/lib/apt/lists/*_Packages*' > /dev/null; then
	apt-get update
fi

mapfile -t pkgs < <(packages "${HERE}/packages/common.list" "${HERE}/packages/${SB_PLATFORM}.list")
echo "customise.sh: installing ${#pkgs[@]} packages for ${SB_PLATFORM}"
apt-get install -y --no-install-recommends "${pkgs[@]}"

# The box's own software, as the syncphony-box package (MAD-810, ADR
# 0012): boxd, the kiosk wrapper, the units, the drop-ins, the initramfs
# overlay scripts, plus the postinst that creates the kiosk user and the
# cec group and enables the units. The nightly update installs newer
# builds of this same package from the apt repo, so the image and the
# updates can't drift apart. Installed before the steps, which build on
# its files.
cp "${DEB}" /tmp/syncphony-box.deb
apt-get install -y --no-install-recommends /tmp/syncphony-box.deb
rm -f /tmp/syncphony-box.deb

for step in "${HERE}"/steps/*.sh; do
	echo "customise.sh: ${step##*/}"
	bash -euo pipefail "${step}"
done
