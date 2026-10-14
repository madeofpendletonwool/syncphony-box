#!/bin/bash -e

# Read-only root and /data (MAD-805, ADR 0007): make the box safe to pull
# the plug on.
#
#   - The overlay: raspi-config's "Overlay FS" mechanism, enabled at build
#     time — /etc/initramfs-tools/scripts/overlay + the overlay module in
#     the initramfs + boot=overlay on the kernel command line. The real
#     root becomes a read-only lower layer; runtime writes land in tmpfs.
#   - /data: a third partition (created by scripts/add-data-partition.sh
#     after export, label syncphony-data) holds everything that must
#     persist — the full list is docs/persistent-paths.md. Here we wire
#     the paths that reach it.
#   - The first-boot grow targets the last partition (our grow-data
#     initramfs script, keyed off its own cmdline token); the stock
#     `resize` token and rpi-resize (which would grow the read-only root)
#     come out.

FILES="$(dirname "$0")/files"

# --- the overlay -------------------------------------------------------------
IIR="${ROOTFS_DIR}/etc/initramfs-tools"
install -v -d -m 0755 "${IIR}/scripts" "${IIR}/scripts/local-premount"
install -v -m 0644 "${FILES}/overlay" "${IIR}/scripts/overlay"
install -v -m 0755 "${FILES}/grow-data" "${IIR}/scripts/local-premount/grow-data"
# The overlay module must be in the initramfs (the script also has an
# insmod fallback off the real root, but don't rely on it).
touch "${IIR}/modules"
grep -qx 'overlay' "${IIR}/modules" || echo overlay >> "${IIR}/modules"
# The initramfs itself is built at export (05-finalise runs
# update-initramfs -k all -c), after this stage, so both of these land
# inside it. No rename/initramfs config.txt line: auto_initramfs=1 loads
# the per-flavour initrd, and boot=overlay (below) activates the script.

# --- the kernel command line --------------------------------------------------
# boot=overlay turns the overlay on. `resize` (the stock first-boot token
# that grows the ROOT partition and rewrites the disk id) comes out: root
# stays at its image size and grow-data takes over for /data.
CMDLINE="${ROOTFS_DIR}/boot/firmware/cmdline.txt"
line="$(tr -d '\r\n' < "${CMDLINE}")"
kept=""
for tok in ${line}; do
	case "${tok}" in
		resize) ;; # stock first-boot root grow — replaced by grow-data
		*) kept="${kept:+${kept} }${tok}" ;;
	esac
done
line="${kept}"
case " ${line} " in
	*" boot=overlay "*) ;;
	*) line="boot=overlay ${line}" ;;
esac
case " ${line} " in
	*" grow-data "*) ;;
	*) line="${line} grow-data" ;;
esac
printf '%s\n' "${line}" > "${CMDLINE}"

# --- fstab --------------------------------------------------------------------
# /boot/firmware read-only: the FAT never sits writable at runtime (boxd
# remounts it rw around its syncphony.txt/cmdline.txt writes only). /data
# with nofail: a card with a destroyed data partition still boots (the
# screen says the storage is lost) instead of hanging in emergency mode.
FSTAB="${ROOTFS_DIR}/etc/fstab"
sed -i '\|/boot/firmware| s/defaults/defaults,ro/' "${FSTAB}"
printf 'DATADEV  /data  ext4  defaults,nofail,x-systemd.device-timeout=15s  0  2\n' >> "${FSTAB}"

# --- paths that live on /data now ---------------------------------------------
# Symlinks are the whole trick for most of them: read early, followed
# wherever the filesystem points, graceful when /data is missing. The
# account files, SSH keys and NetworkManager connections can't be symlinks
# (rewritten atomically, read too early) — data-persist copies those.
install -v -d -m 0755 "${ROOTFS_DIR}/data"

rm -rf "${ROOTFS_DIR}/var/log/journal"
ln -v -sfn /data/journal "${ROOTFS_DIR}/var/log/journal"

rm -rf "${ROOTFS_DIR}/var/lib/cloud"
ln -v -sfn /data/system/cloud "${ROOTFS_DIR}/var/lib/cloud"

# config.env: boxd writes the real file on /data; the /etc path stays a
# symlink so the kiosk wrapper and the reset tool keep reading it there.
ln -v -sfn /data/syncphony-box/config.env "${ROOTFS_DIR}/etc/syncphony-box/config.env"

# SSH host keys: dangling until data-persist restore / ssh-keygen -A
# creates the targets on /data (through these symlinks).
install -v -d -m 0755 "${ROOTFS_DIR}/etc/ssh"
for type in rsa ecdsa ed25519; do
	for suffix in "" ".pub"; do
		key="ssh_host_${type}_key${suffix}"
		rm -f "${ROOTFS_DIR}/etc/ssh/${key}"
		ln -v -sfn "/data/system/ssh/${key}" "${ROOTFS_DIR}/etc/ssh/${key}"
	done
done

# --- units and tools ------------------------------------------------------------
install -v -m 0755 "${FILES}/data-persist" "${ROOTFS_DIR}/usr/lib/syncphony-box/data-persist"
install -v -d -m 0755 "${ROOTFS_DIR}/etc/systemd/journald.conf.d"
install -v -m 0644 "${FILES}/50-syncphony-journald.conf" \
	"${ROOTFS_DIR}/etc/systemd/journald.conf.d/50-syncphony-journald.conf"
for unit in syncphony-data-grow.service syncphony-data-prepare.service \
	syncphony-data-save.service syncphony-data-save.timer; do
	install -v -m 0644 "${FILES}/${unit}" "${ROOTFS_DIR}/etc/systemd/system/${unit}"
done

on_chroot << EOF
# The stock first-boot grow targets the root partition; ours targets the
# last one. Disable the stock unit (the resize token is already out of the
# command line above).
systemctl disable rpi-resize.service
# Package downloads/upgrades onto a tmpfs overlay would vanish at reboot
# while looking applied — the one safe update path is writing the real
# root (sudo syncphony-box chroot; ADR 0007), so the timers stay off.
systemctl disable unattended-upgrades.timer apt-daily.timer apt-daily-upgrade.timer || true
systemctl enable syncphony-data-grow.service syncphony-data-prepare.service \
	syncphony-data-save.service syncphony-data-save.timer
EOF
