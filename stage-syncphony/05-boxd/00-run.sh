#!/bin/bash -e

# boxd: the Syncphony box's helper daemon (MAD-803, ADR 0005). It owns
# everything that isn't Chromium: the syncphony.txt config (the boot apply
# runs as syncphony-box-config.service), the box bridge on loopback
# (Syncphony ADR 0016) and the local setup/offline screens the kiosk opens.
#
# The arm64 binary is built by ./build.sh into files/ (it is not committed);
# running pi-gen directly without it is a mistake we catch here.

if [ ! -f files/boxd ]; then
	echo "stage-syncphony/05-boxd: files/boxd is missing." >&2
	echo "Build it first: ./build.sh (or: cd boxd && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o ../stage-syncphony/05-boxd/files/boxd ./cmd/boxd)" >&2
	exit 1
fi

install -v -m 0755 files/boxd "${ROOTFS_DIR}/usr/lib/syncphony-box/boxd"
install -v -m 0644 files/boxd.service "${ROOTFS_DIR}/etc/systemd/system/boxd.service"

on_chroot << EOF
systemctl enable boxd.service
EOF
