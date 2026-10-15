#!/bin/bash -e

# The Pi's boot partition. MAD-846 split this out of the shared layer; the
# x86 installer does the same jobs for GRUB and the EFI partition.
#
#   - Boot presentation: quiet kernel and console, no blinking cursor, no
#     console blanking, and no firmware rainbow splash. The TV shows
#     nothing but the kiosk. Cage itself never blanks or DPMS-es its
#     output.
#   - The read-only root (MAD-805, ADR 0007): boot=overlay turns the
#     initramfs overlay on; the stock first-boot `resize` (which grows the
#     ROOT partition) comes out and grow-data (which grows /data) goes in.
#     /boot/firmware is mounted read-only (boxd remounts it around its
#     syncphony.txt and cmdline.txt writes only).
#   - The boot config file (ADR 0003): syncphony.txt ships on the FAT boot
#     partition, every option commented out, so it can be edited from any
#     computer. pi-gen's export copies /boot/firmware onto the FAT.

CMDLINE="${ROOTFS_DIR}/boot/firmware/cmdline.txt"
CONFIG="${ROOTFS_DIR}/boot/firmware/config.txt"

# --- the kernel command line --------------------------------------------------
line="$(tr -d '\r\n' < "${CMDLINE}")"
kept=""
for tok in ${line}; do
	case "${tok}" in
		resize) ;; # stock first-boot root grow, replaced by grow-data
		*) kept="${kept:+${kept} }${tok}" ;;
	esac
done
line="${kept}"
case " ${line} " in
	*" boot=overlay "*) ;;
	*) line="boot=overlay ${line}" ;;
esac
for param in quiet splash loglevel=3 vt.global_cursor_default=0 consoleblank=0 grow-data; do
	case " ${line} " in
		*" ${param} "*) ;;
		*) line="${line} ${param}" ;;
	esac
done
printf '%s\n' "${line}" > "${CMDLINE}"

# --- config.txt ---------------------------------------------------------------
# config.txt ends inside an [all] section, so appended lines apply to every
# board. disable_splash turns off the firmware rainbow screen. No
# rename/initramfs line: auto_initramfs=1 loads the per-flavour initrd,
# which pi-gen's export builds (05-finalise runs update-initramfs) with
# the overlay script and module inside.
if [ -n "$(tail -c 1 "${CONFIG}")" ]; then
	printf '\n' >> "${CONFIG}"
fi
grep -q '^disable_splash=1' "${CONFIG}" || printf 'disable_splash=1\n' >> "${CONFIG}"

# --- fstab: the boot partition read-only ---------------------------------------
grep -q '/boot/firmware.*defaults,ro' "${ROOTFS_DIR}/etc/fstab" ||
	sed -i '\|/boot/firmware| s/defaults/defaults,ro/' "${ROOTFS_DIR}/etc/fstab"

# --- syncphony.txt --------------------------------------------------------------
install -v -m 0644 "${ROOTFS_DIR}/usr/lib/syncphony-box/syncphony.txt" \
	"${ROOTFS_DIR}/boot/firmware/syncphony.txt"

# --- the stock root grow ----------------------------------------------------------
# It targets the root partition; ours (grow-data) targets the last one.
on_chroot << EOF
systemctl disable rpi-resize.service
EOF
