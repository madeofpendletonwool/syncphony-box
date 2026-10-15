#!/bin/bash -e

# The box's own software, as the syncphony-box package (MAD-810, ADR
# 0012): boxd, the kiosk wrapper, the units, the drop-ins, the initramfs
# overlay scripts — everything in packaging/root — plus the postinst that
# creates the kiosk user and the cec group and enables the units. The
# nightly update installs newer builds of this same package from the apt
# repo, so the image and the updates can't drift apart.
#
# Installed here, right after the kiosk stack, so the later substages can
# build on its files (the boot-partition syncphony.txt, the kiosk user's
# home, the initramfs module list).
#
# ./build.sh builds the .deb into files/ (it is not committed).

if [ ! -f files/syncphony-box.deb ]; then
	echo "stage-syncphony/00-packages: files/syncphony-box.deb is missing." >&2
	echo "Build the image with ./build.sh, which builds boxd and the package first." >&2
	exit 1
fi

install -v -m 0644 files/syncphony-box.deb "${ROOTFS_DIR}/tmp/syncphony-box.deb"
on_chroot << EOF
apt-get install -y /tmp/syncphony-box.deb
rm -f /tmp/syncphony-box.deb
EOF
