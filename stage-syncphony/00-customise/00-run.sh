#!/bin/bash -e

# The pi-gen side of the shared customise layer (MAD-846, ADR 0013). The
# box itself is rootfs/customise.sh: packages, the syncphony-box .deb,
# the kiosk user's audio, the read-only-root links, the units and the
# slimming. The x86 ISO build runs the same script. This wrapper only
# gets it into the chroot and runs it there.
#
# ./build.sh copies rootfs/ and the freshly built .deb into files/ (not
# committed).

if [ ! -f files/syncphony-box.deb ] || [ ! -x files/rootfs/customise.sh ]; then
	echo "stage-syncphony/00-customise: files/ is missing the .deb or rootfs/." >&2
	echo "Build the image with ./build.sh, which builds boxd and the package first." >&2
	exit 1
fi

rm -rf "${ROOTFS_DIR}/tmp/syncphony-rootfs"
cp -a files/rootfs "${ROOTFS_DIR}/tmp/syncphony-rootfs"
install -v -m 0644 files/syncphony-box.deb "${ROOTFS_DIR}/tmp/syncphony-rootfs/syncphony-box.deb"

on_chroot << EOF
/tmp/syncphony-rootfs/customise.sh pi /tmp/syncphony-rootfs/syncphony-box.deb
rm -rf /tmp/syncphony-rootfs
EOF
