#!/bin/bash -e

# Audio: PipeWire runs for the kiosk user (started at boot via linger, no
# login), and audio-setup picks the sink from audio= in syncphony.txt,
# sets it to 100% and follows hotplug. See docs/adr/0004-audio-output.md.

install -v -d -m 0755 "${ROOTFS_DIR}/usr/lib/syncphony-box"
install -v -m 0755 files/audio-setup "${ROOTFS_DIR}/usr/lib/syncphony-box/audio-setup"
install -v -m 0644 files/syncphony-audio.service "${ROOTFS_DIR}/etc/systemd/system/syncphony-audio.service"
install -v -m 0644 files/syncphony-audio-monitor.service "${ROOTFS_DIR}/etc/systemd/system/syncphony-audio-monitor.service"

# PipeWire's user services for the kiosk user. Linger makes systemd start
# the kiosk user's manager (user@<uid>.service) at boot without a login;
# the wants symlinks below pull the audio stack into it.
install -v -d -m 0755 "${ROOTFS_DIR}/var/lib/systemd/linger"
touch "${ROOTFS_DIR}/var/lib/systemd/linger/kiosk"

USER_UNITS="${ROOTFS_DIR}/var/lib/syncphony-box/.config/systemd/user"
install -v -d -m 0755 "${USER_UNITS}/default.target.wants" "${USER_UNITS}/sockets.target.wants"
for unit in pipewire.service wireplumber.service pipewire-pulse.service; do
	ln -v -sfn "/usr/lib/systemd/user/${unit}" "${USER_UNITS}/default.target.wants/${unit}"
done
for unit in pipewire.socket pipewire-pulse.socket; do
	ln -v -sfn "/usr/lib/systemd/user/${unit}" "${USER_UNITS}/sockets.target.wants/${unit}"
done

# Keep the outputs from auto-suspending: waking an ALSA device for the
# first sound can clip its opening notes (audible between tracks).
install -v -d -m 0755 "${ROOTFS_DIR}/var/lib/syncphony-box/.config/wireplumber/wireplumber.conf.d"
install -v -m 0644 files/50-syncphony-box.conf "${ROOTFS_DIR}/var/lib/syncphony-box/.config/wireplumber/wireplumber.conf.d/50-syncphony-box.conf"

on_chroot << EOF
chown -R kiosk:kiosk /var/lib/syncphony-box/.config
systemctl enable syncphony-audio.service syncphony-audio-monitor.service
EOF
