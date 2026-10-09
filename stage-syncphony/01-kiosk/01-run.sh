#!/bin/bash -e

# The kiosk session: an unprivileged `kiosk` user runs cage + Chromium on
# tty1 under syncphony-kiosk.service. See docs/adr/0002-kiosk-session.md.

# System user: locked password, no shell, no sudo. Seat-device access comes
# from the logind session (PAMName=login) plus these supplementary groups.
on_chroot << EOF
if ! getent passwd kiosk >/dev/null; then
	adduser --system --group --home /var/lib/syncphony-box --shell /usr/sbin/nologin kiosk
fi
for grp in video render audio input; do
	adduser kiosk "\$grp"
done
EOF

install -v -d -m 0755 "${ROOTFS_DIR}/usr/lib/syncphony-box"
install -v -m 0755 files/kiosk "${ROOTFS_DIR}/usr/lib/syncphony-box/kiosk"
install -v -m 0644 files/unconfigured.html "${ROOTFS_DIR}/usr/lib/syncphony-box/unconfigured.html"
install -v -m 0644 files/syncphony-kiosk.service "${ROOTFS_DIR}/etc/systemd/system/syncphony-kiosk.service"

# Where the server URL is read from until the boot config file
# (syncphony.txt) lands; the kiosk wrapper reads it at start.
install -v -d -m 0755 "${ROOTFS_DIR}/etc/syncphony-box"

on_chroot << EOF
install -d -o kiosk -g kiosk -m 0755 /var/lib/syncphony-box
systemctl enable syncphony-kiosk.service
EOF
