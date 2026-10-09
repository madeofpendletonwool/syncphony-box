#!/bin/bash -e

# Boot presentation: quiet kernel and console, no blinking cursor, no
# console blanking, and no firmware rainbow splash. The TV shows nothing
# but the kiosk. Cage itself never blanks or DPMS-es its output.

CMDLINE="${ROOTFS_DIR}/boot/firmware/cmdline.txt"
CONFIG="${ROOTFS_DIR}/boot/firmware/config.txt"

line="$(tr -d '\r\n' < "${CMDLINE}")"
for param in quiet splash loglevel=3 vt.global_cursor_default=0 consoleblank=0; do
	case " ${line} " in
		*" ${param} "*) ;;
		*) line="${line} ${param}" ;;
	esac
done
printf '%s\n' "${line}" > "${CMDLINE}"

# config.txt ends inside an [all] section, so appended lines apply to every
# board. disable_splash turns off the firmware rainbow screen.
if [ -n "$(tail -c 1 "${CONFIG}")" ]; then
	printf '\n' >> "${CONFIG}"
fi
grep -q '^disable_splash=1' "${CONFIG}" || printf 'disable_splash=1\n' >> "${CONFIG}"
