#!/bin/bash -e

# The boot config file: syncphony.txt ships on the FAT boot partition so
# it can be edited from any computer. The parsing and applying live in boxd
# (boxd config apply, run by syncphony-box-config.service); boxd itself is
# installed by 05-boxd. See docs/adr/0003-box-config-file.md and 0005-boxd.md.

install -v -d -m 0755 "${ROOTFS_DIR}/etc/syncphony-box"
install -v -m 0644 files/syncphony-box-config.service "${ROOTFS_DIR}/etc/systemd/system/syncphony-box-config.service"

# Every option ships commented out; boxd's config apply fills in defaults.
# pi-gen's export step copies /boot/firmware onto the FAT partition.
install -v -m 0644 files/syncphony.txt "${ROOTFS_DIR}/boot/firmware/syncphony.txt"

on_chroot << EOF
systemctl enable syncphony-box-config.service
EOF
