#!/bin/bash -e

# HDMI-CEC (MAD-807, ADR 0009). The pieces at the image level:
#
#   - v4l-utils (00-packages): cec-ctl, boxd's whole CEC interface to the
#     kernel framework.
#   - The cec group and its udev rule: /dev/cec* and /dev/uinput grant
#     access by group, not by root — boxd gets them through
#     SupplementaryGroups (the drop-in below).
#   - uinput in modules-load.d, so the injected keyboard's device node
#     exists before boxd starts.
#
# boxd itself (registering as a playback device, the monitor, the
# wake/switch rate limit, the remote keys, the OK long press) lives in
# boxd/internal/cec.

FILES="$(dirname "$0")/files"

on_chroot << EOF
getent group cec >/dev/null || groupadd --system cec
EOF

install -v -d -m 0755 "${ROOTFS_DIR}/etc/udev/rules.d"
install -v -m 0644 "${FILES}/60-syncphony-cec.rules" \
	"${ROOTFS_DIR}/etc/udev/rules.d/60-syncphony-cec.rules"

install -v -d -m 0755 "${ROOTFS_DIR}/etc/systemd/system/boxd.service.d"
install -v -m 0644 "${FILES}/50-cec.conf" \
	"${ROOTFS_DIR}/etc/systemd/system/boxd.service.d/50-cec.conf"

install -v -d -m 0755 "${ROOTFS_DIR}/etc/modules-load.d"
install -v -m 0644 "${FILES}/uinput.conf" \
	"${ROOTFS_DIR}/etc/modules-load.d/syncphony-cec.conf"
