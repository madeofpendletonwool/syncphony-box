#!/usr/bin/env bash
# Smoke-test a built syncphony-box image without hardware.
#
# Usage: scripts/smoke-test.sh deploy/syncphony-box-<version>-arm64.img.xz
#
# Loop-mounts the image's two partitions and checks that the kiosk packages
# are installed, the cloud-init first-boot machinery (which applies Raspberry
# Pi Imager settings) is enabled, the first user is locked, the NoCloud
# seed files are present on the boot partition, the kiosk session is in
# place (user, unit, wrapper, boot tuning), the boot config file
# (syncphony.txt) ships on the boot partition fully commented out, and the
# audio stack is in place (PipeWire user services for the kiosk user with
# linger, the sink-selection units, the WirePlumber tuning).
#
# Later issues extend this test as the image grows (boxd, /data, ...).

set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <image>.img.xz" >&2
	exit 2
fi

IMAGE_XZ="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
WORK="$(mktemp -d /tmp/syncphony-box-smoke.XXXXXX)"
LOOP=""

cleanup() {
	sudo umount "${WORK}/boot" "${WORK}/root" 2>/dev/null || true
	if [ -n "${LOOP}" ]; then
		sudo losetup -d "${LOOP}" 2>/dev/null || true
	fi
	rm -rf "${WORK}"
}
trap cleanup EXIT

failures=0

check() {
	local name="$1" result="$2"
	if [ "${result}" = "ok" ]; then
		echo "PASS: ${name}"
	else
		echo "FAIL: ${name} (${result})" >&2
		failures=$((failures + 1))
	fi
}

echo "Decompressing ${IMAGE_XZ} ..."
xz -dk -T0 -c "${IMAGE_XZ}" > "${WORK}/image.img"

LOOP="$(sudo losetup --find --show --partscan "${WORK}/image.img")"
mkdir -p "${WORK}/boot" "${WORK}/root"
sudo mount -o ro "${LOOP}p1" "${WORK}/boot"
sudo mount -o ro "${LOOP}p2" "${WORK}/root"

# --- packages ---------------------------------------------------------------
for pkg in cage chromium fonts-noto-color-emoji libgl1-mesa-dri cloud-init avahi-daemon \
	pipewire pipewire-bin pipewire-alsa pipewire-pulse wireplumber pulseaudio-utils alsa-utils; do
	if sudo awk -v p="${pkg}" '
		$0 == "Package: " p { inpkg = 1 }
		inpkg && /^Status: / {
			if ($0 == "Status: install ok installed") ok = 1
			inpkg = 0
		}
		END { exit ok ? 0 : 1 }
	' "${WORK}/root/var/lib/dpkg/status"; then
		check "package ${pkg} installed" ok
	else
		check "package ${pkg} installed" "missing or not installed"
	fi
done

# --- units ------------------------------------------------------------------
for unit in cloud-config.service cloud-final.service syncphony-kiosk.service syncphony-box-config.service \
	avahi-daemon.service syncphony-audio.service syncphony-audio-monitor.service; do
	state="$(systemctl --root="${WORK}/root" is-enabled "${unit}" 2>/dev/null || true)"
	if [ "${state}" = "enabled" ]; then
		check "unit ${unit} enabled" ok
	else
		check "unit ${unit} enabled" "${state:-not-found}"
	fi
done

# --- kiosk session ----------------------------------------------------------
if sudo grep -q '^kiosk:' "${WORK}/root/etc/passwd"; then
	check "kiosk user exists" ok
else
	check "kiosk user exists" "missing"
fi

kiosk_shadow="$(sudo sed -n 's/^kiosk:\([^:]*\):.*/\1/p' "${WORK}/root/etc/shadow")"
case "${kiosk_shadow}" in
	'*'|'!'|'!*') check "kiosk user password locked" ok ;;
	'') check "kiosk user password locked" "no shadow entry" ;;
	*) check "kiosk user password locked" "password field: ${kiosk_shadow}" ;;
esac

for grp in video render audio input; do
	if sudo grep -E "^${grp}:" "${WORK}/root/etc/group" | grep -q kiosk; then
		check "kiosk user in ${grp} group" ok
	else
		check "kiosk user in ${grp} group" "not a member"
	fi
done

if sudo test -x "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "kiosk wrapper installed executable" ok
else
	check "kiosk wrapper installed executable" "missing or not executable"
fi

if sudo test -f "${WORK}/root/usr/lib/syncphony-box/unconfigured.html"; then
	check "unconfigured fallback page installed" ok
else
	check "unconfigured fallback page installed" "missing"
fi

# --- boot config (syncphony.txt) --------------------------------------------
if sudo test -x "${WORK}/root/usr/lib/syncphony-box/syncphony-box-config"; then
	check "config parser installed executable" ok
else
	check "config parser installed executable" "missing or not executable"
fi

if sudo grep -q 'config.env' "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "kiosk wrapper reads config.env" ok
else
	check "kiosk wrapper reads config.env" "no reference found"
fi

if [ -f "${WORK}/boot/syncphony.txt" ]; then
	check "boot partition has syncphony.txt" ok
else
	check "boot partition has syncphony.txt" "missing"
fi

# The file must ship fully commented out: every non-comment line is empty.
active="$(sudo grep -Ev '^[[:space:]]*(#|$)' "${WORK}/boot/syncphony.txt" 2>/dev/null || true)"
if [ -z "${active}" ]; then
	check "syncphony.txt ships fully commented out" ok
else
	check "syncphony.txt ships fully commented out" "active lines: ${active}"
fi

for key in server_url name audio resolution rotate cec; do
	if sudo grep -q "^#${key}=" "${WORK}/boot/syncphony.txt"; then
		check "syncphony.txt documents ${key}" ok
	else
		check "syncphony.txt documents ${key}" "no commented ${key}= line"
	fi
done

# --- audio (PipeWire for the kiosk user, sink from audio=) -------------------
if sudo test -x "${WORK}/root/usr/lib/syncphony-box/audio-setup"; then
	check "audio setup installed executable" ok
else
	check "audio setup installed executable" "missing or not executable"
fi

# Linger starts the kiosk user's PipeWire at boot, without a login.
if sudo test -f "${WORK}/root/var/lib/systemd/linger/kiosk"; then
	check "kiosk user lingers (PipeWire at boot)" ok
else
	check "kiosk user lingers (PipeWire at boot)" "no linger file"
fi

USER_UNITS="var/lib/syncphony-box/.config/systemd/user"
for unit in default.target.wants/pipewire.service default.target.wants/wireplumber.service \
	default.target.wants/pipewire-pulse.service sockets.target.wants/pipewire.socket \
	sockets.target.wants/pipewire-pulse.socket; do
	if sudo test -e "${WORK}/root/${USER_UNITS}/${unit}" &&
		sudo test -e "${WORK}/root/usr/lib/systemd/user/${unit##*/}"; then
		check "kiosk user unit ${unit##*/} enabled" ok
	else
		check "kiosk user unit ${unit##*/} enabled" "symlink or target missing"
	fi
done

if sudo grep -q 'session.suspend-timeout-seconds = 0' \
	"${WORK}/root/var/lib/syncphony-box/.config/wireplumber/wireplumber.conf.d/50-syncphony-box.conf"; then
	check "wireplumber keeps outputs unsuspended" ok
else
	check "wireplumber keeps outputs unsuspended" "conf missing or rule absent"
fi

if sudo grep -q 'syncphony-audio.service' "${WORK}/root/etc/systemd/system/syncphony-kiosk.service"; then
	check "kiosk waits for the audio unit" ok
else
	check "kiosk waits for the audio unit" "no reference in syncphony-kiosk.service"
fi

# --- boot presentation ------------------------------------------------------
CMDLINE=" $(sudo cat "${WORK}/boot/cmdline.txt") "
for param in quiet splash loglevel=3 vt.global_cursor_default=0 consoleblank=0; do
	if [ "${CMDLINE}" != "${CMDLINE/ ${param} /}" ]; then
		check "cmdline has ${param}" ok
	else
		check "cmdline has ${param}" "missing"
	fi
done

if sudo grep -q '^disable_splash=1' "${WORK}/boot/config.txt"; then
	check "config.txt has disable_splash=1" ok
else
	check "config.txt has disable_splash=1" "missing"
fi

# --- first user is locked (no password until Imager settings apply) ---------
if sudo grep -q '^syncphony:!' "${WORK}/root/etc/shadow"; then
	check "first user syncphony has locked password" ok
else
	check "first user syncphony has locked password" "not locked"
fi

# --- Imager first-boot seed files on the boot partition ---------------------
for file in user-data network-config meta-data; do
	if [ -f "${WORK}/boot/${file}" ]; then
		check "boot partition has ${file}" ok
	else
		check "boot partition has ${file}" "missing"
	fi
done

if [ "${failures}" -ne 0 ]; then
	echo "${failures} check(s) failed" >&2
	exit 1
fi

echo "All checks passed."
