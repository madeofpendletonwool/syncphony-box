#!/bin/bash -e

# The boot config file: syncphony.txt ships on the FAT boot partition so
# it can be edited from any computer, and a oneshot unit parses it into
# /etc/syncphony-box/config.env before the kiosk starts on every boot.
# See docs/adr/0003-box-config-file.md.

install -v -d -m 0755 "${ROOTFS_DIR}/usr/lib/syncphony-box" "${ROOTFS_DIR}/etc/syncphony-box"
install -v -m 0755 files/syncphony-box-config "${ROOTFS_DIR}/usr/lib/syncphony-box/syncphony-box-config"
install -v -m 0644 files/syncphony-box-config.service "${ROOTFS_DIR}/etc/systemd/system/syncphony-box-config.service"

# Every option ships commented out; the config service fills in defaults.
# pi-gen's export step copies /boot/firmware onto the FAT partition.
install -v -m 0644 files/syncphony.txt "${ROOTFS_DIR}/boot/firmware/syncphony.txt"

on_chroot << EOF
systemctl enable syncphony-box-config.service
EOF
