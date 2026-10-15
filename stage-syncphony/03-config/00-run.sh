#!/bin/bash -e

# The boot config file: syncphony.txt ships on the FAT boot partition so
# it can be edited from any computer. The parsing and applying live in boxd
# (boxd config apply, run by syncphony-box-config.service); both come with
# the syncphony-box package (00-packages), which also carries the template.
# See docs/adr/0003-box-config-file.md and 0005-boxd.md.

# Every option ships commented out; boxd's config apply fills in defaults.
# pi-gen's export step copies /boot/firmware onto the FAT partition.
install -v -m 0644 "${ROOTFS_DIR}/usr/lib/syncphony-box/syncphony.txt" \
	"${ROOTFS_DIR}/boot/firmware/syncphony.txt"
