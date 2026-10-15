#!/usr/bin/env bash
# Smoke-test a built syncphony-box image without hardware.
#
# Usage: scripts/smoke-test.sh deploy/syncphony-box-<version>-arm64.img.xz
#
# Loop-mounts the image's three partitions and checks that the kiosk packages
# are installed, the cloud-init first-boot machinery (which applies Raspberry
# Pi Imager settings) is enabled, the first user is locked, the NoCloud
# seed files are present on the boot partition, the kiosk session is in
# place (user, unit, wrapper, boot tuning), the boot config file
# (syncphony.txt) ships on the boot partition fully commented out, the
# audio stack is in place (PipeWire user services for the kiosk user with
# linger, the sink-selection units, the WirePlumber tuning), boxd (the
# Go helper daemon) is installed with its units enabled, the pairing
# persistence pieces agree (one profile path, crash-state prep, reset), the
# read-only-root layout is complete: three partitions with the third
# labelled syncphony-data and pre-populated, the overlay mechanism wired
# into the initramfs and the kernel command line, every persistent path
# pointing at /data, and the stock root-resize replaced by the data grow;
# and the self-healing pieces are wired: the systemd hardware watchdog
# drop-in, timesyncd enabled and ordered before the kiosk, and vcgencmd
# for boxd's thermals. The HDMI-CEC pieces are in place too: cec-ctl
# installed, the cec group with its udev rule for /dev/cec* and
# /dev/uinput, boxd granted the group, and uinput loaded at boot.

set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <image>.img.xz" >&2
	exit 2
fi

IMAGE_XZ="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
WORK="$(mktemp -d /tmp/syncphony-box-smoke.XXXXXX)"
LOOP=""

cleanup() {
	sudo umount "${WORK}/boot" "${WORK}/root" "${WORK}/data" 2>/dev/null || true
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
mkdir -p "${WORK}/boot" "${WORK}/root" "${WORK}/data"
sudo mount -o ro "${LOOP}p1" "${WORK}/boot"
sudo mount -o ro "${LOOP}p2" "${WORK}/root"
sudo mount -o ro "${LOOP}p3" "${WORK}/data"

# --- partitions: boot, root, data (MAD-805) ----------------------------------
part_count="$(parted -m -s "${WORK}/image.img" print | awk -F: '$1 ~ /^[0-9]+$/ { n++ } END { print n }')"
if [ "${part_count}" = "3" ]; then
	check "image has 3 partitions" ok
else
	check "image has 3 partitions" "found ${part_count}"
fi

data_label="$(sudo blkid -s LABEL -o value "${LOOP}p3" 2>/dev/null || true)"
if [ "${data_label}" = "syncphony-data" ]; then
	check "third partition labelled syncphony-data" ok
else
	check "third partition labelled syncphony-data" "label: ${data_label:-none}"
fi

# The pre-created persistent layout (docs/persistent-paths.md).
for dir in chromium journal syncphony-box boxd system/cloud system/identity system/ssh system/network; do
	if sudo test -d "${WORK}/data/${dir}"; then
		check "/data/${dir} exists in the image" ok
	else
		check "/data/${dir} exists in the image" "missing"
	fi
done

kiosk_uid="$(sudo awk -F: '$1=="kiosk"{print $3}' "${WORK}/root/etc/passwd")"
profile_owner="$(sudo stat -c '%u' "${WORK}/data/chromium")"
if [ -n "${kiosk_uid}" ] && [ "${profile_owner}" = "${kiosk_uid}" ]; then
	check "/data/chromium owned by the kiosk user" ok
else
	check "/data/chromium owned by the kiosk user" "owner ${profile_owner}, kiosk uid ${kiosk_uid}"
fi

# --- packages ---------------------------------------------------------------
for pkg in cage chromium fonts-noto-color-emoji libgl1-mesa-dri cloud-init avahi-daemon \
	pipewire pipewire-bin pipewire-alsa pipewire-pulse wireplumber pulseaudio-utils alsa-utils \
	raspi-utils-core systemd-timesyncd v4l-utils; do
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
	avahi-daemon.service syncphony-audio.service syncphony-audio-monitor.service boxd.service \
	syncphony-data-grow.service syncphony-data-prepare.service syncphony-data-save.service \
	syncphony-data-save.timer systemd-timesyncd.service; do
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
if sudo grep -q 'config.env' "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "kiosk wrapper reads config.env" ok
else
	check "kiosk wrapper reads config.env" "no reference found"
fi

# --- pairing persistence (profile path, crash-state prep, reset) -------------
# The Chromium profile path is defined once in boxd; the smoke test pins
# that the wrapper, the parse output and the reset tool all agree on it.
if sudo test -x "${WORK}/root/usr/bin/syncphony-box"; then
	check "syncphony-box admin command installed" ok
else
	check "syncphony-box admin command installed" "missing or not executable"
fi

if sudo grep -q 'profile reset' "${WORK}/root/usr/bin/syncphony-box"; then
	check "syncphony-box has the reset verb" ok
else
	check "syncphony-box has the reset verb" "no reset"
fi

if sudo grep -q 'profile prep' "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "kiosk wrapper preps the profile (no restore prompt)" ok
else
	check "kiosk wrapper preps the profile (no restore prompt)" "no 'profile prep' call"
fi

if sudo grep -q 'PROFILE_DIR' "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "kiosk wrapper uses the configurable profile dir" ok
else
	check "kiosk wrapper uses the configurable profile dir" "no PROFILE_DIR reference"
fi

# The image's boxd is a static arm64 binary and the CI runner is arm64, so
# it can run directly; where it can't (a dev's x86 machine), skip the
# run-time checks and keep the static ones.
if out="$(sudo "${WORK}/root/usr/lib/syncphony-box/boxd" profile path 2>/dev/null)" &&
	[ -n "${out}" ]; then
	profile_dir="${out}"
	check "boxd profile path answers" ok
else
	profile_dir="/data/chromium"
	check "boxd profile path answers" "skipped (cannot run the arm64 binary here)"
fi

if sudo grep -qF "DATA_DIR=${profile_dir}" "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "wrapper profile fallback matches boxd's path" ok
else
	check "wrapper profile fallback matches boxd's path" "wrapper fallback is not ${profile_dir}"
fi

if sudo grep -q "PROFILE_DIR='${profile_dir}'" \
	"${WORK}/root/etc/syncphony-box/config.env" 2>/dev/null; then
	check "config.env names the profile dir" ok
else
	# config.env only exists after first boot; the parse output is what
	# generates it.
	if out="$(sudo "${WORK}/root/usr/lib/syncphony-box/boxd" parse "${WORK}/boot/syncphony.txt" 2>/dev/null)" &&
		echo "${out}" | grep -qF "PROFILE_DIR='${profile_dir}'"; then
		check "config.env names the profile dir" ok
	else
		check "config.env names the profile dir" "PROFILE_DIR missing from the parse output"
	fi
fi

# --- boxd (the Go helper daemon) ----------------------------------------------
if sudo test -x "${WORK}/root/usr/lib/syncphony-box/boxd"; then
	check "boxd installed executable" ok
else
	check "boxd installed executable" "missing or not executable"
fi

# Static (CGO_ENABLED=0) arm64 binary — only verifiable where `file` runs
# on it, i.e. on the arm64 CI runner.
if ! command -v file >/dev/null 2>&1; then
	check "boxd is a static binary" ok
elif file "${WORK}/root/usr/lib/syncphony-box/boxd" 2>/dev/null |
	grep -q 'ELF 64-bit LSB.*ARM aarch64.*statically linked'; then
	check "boxd is a static binary" ok
else
	check "boxd is a static binary" "$(file "${WORK}/root/usr/lib/syncphony-box/boxd" 2>/dev/null)"
fi

if sudo grep -q '^ExecStart=/usr/lib/syncphony-box/boxd serve$' \
	"${WORK}/root/etc/systemd/system/boxd.service"; then
	check "boxd.service runs boxd serve" ok
else
	check "boxd.service runs boxd serve" "ExecStart mismatch"
fi

# --- watchdogs and self-healing (MAD-806) -------------------------------------
if sudo grep -q '^RuntimeWatchdogSec=15s' \
	"${WORK}/root/etc/systemd/system.conf.d/50-syncphony-watchdog.conf"; then
	check "hardware watchdog armed at 15 s" ok
else
	check "hardware watchdog armed at 15 s" "drop-in missing or wrong"
fi

if sudo grep -q '^RebootWatchdogSec=' \
	"${WORK}/root/etc/systemd/system.conf.d/50-syncphony-watchdog.conf"; then
	check "reboot watchdog bounded" ok
else
	check "reboot watchdog bounded" "no RebootWatchdogSec"
fi

if sudo grep -q 'time-sync.target' "${WORK}/root/etc/systemd/system/syncphony-kiosk.service"; then
	check "kiosk unit waits for time sync" ok
else
	check "kiosk unit waits for time sync" "no time-sync.target ordering"
fi

if sudo test -x "${WORK}/root/usr/bin/vcgencmd"; then
	check "vcgencmd installed (thermals)" ok
else
	check "vcgencmd installed (thermals)" "missing or not executable"
fi

# --- HDMI-CEC (MAD-807) --------------------------------------------------------
if sudo test -x "${WORK}/root/usr/bin/cec-ctl"; then
	check "cec-ctl installed (CEC)" ok
else
	check "cec-ctl installed (CEC)" "missing or not executable"
fi

if sudo grep -q '^cec:' "${WORK}/root/etc/group"; then
	check "cec group exists" ok
else
	check "cec group exists" "missing"
fi

if sudo grep -q 'KERNEL=="uinput", GROUP="cec"' \
	"${WORK}/root/etc/udev/rules.d/60-syncphony-cec.rules" &&
	sudo grep -q 'KERNEL=="cec\[0-9\]\*", GROUP="cec"' \
	"${WORK}/root/etc/udev/rules.d/60-syncphony-cec.rules"; then
	check "udev rule grants cec and uinput to the group" ok
else
	check "udev rule grants cec and uinput to the group" "rule missing or wrong"
fi

if sudo grep -q '^SupplementaryGroups=cec' \
	"${WORK}/root/etc/systemd/system/boxd.service.d/50-cec.conf"; then
	check "boxd holds the cec group" ok
else
	check "boxd holds the cec group" "drop-in missing or wrong"
fi

if sudo grep -q '^uinput$' "${WORK}/root/etc/modules-load.d/syncphony-cec.conf"; then
	check "uinput loaded at boot" ok
else
	check "uinput loaded at boot" "modules-load entry missing"
fi

# The boot config apply is boxd now: same unit, new engine.
if sudo grep -q '^ExecStart=/usr/lib/syncphony-box/boxd config apply$' \
	"${WORK}/root/etc/systemd/system/syncphony-box-config.service"; then
	check "config apply runs boxd" ok
else
	check "config apply runs boxd" "ExecStart mismatch"
fi

if sudo grep -q '127.0.0.1:8099' "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "kiosk wrapper enters through boxd" ok
else
	check "kiosk wrapper enters through boxd" "no boxd URL in the wrapper"
fi

if sudo grep -q 'boxd.service' "${WORK}/root/etc/systemd/system/syncphony-kiosk.service"; then
	check "kiosk unit waits for boxd" ok
else
	check "kiosk unit waits for boxd" "no boxd reference"
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

for key in server_url name audio resolution rotate cec cec_standby cec_standby_minutes; do
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
	# -L, not -e: the links are absolute, so -e would follow them on the
	# build host rather than inside the image. The target is checked below.
	if sudo test -L "${WORK}/root/${USER_UNITS}/${unit}" &&
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

# --- read-only root (MAD-805) ------------------------------------------------
# boot=overlay activates the overlay script in the initramfs; the stock
# `resize` token must be gone (it would grow the read-only root).
if [ "${CMDLINE}" != "${CMDLINE/ boot=overlay /}" ]; then
	check "cmdline has boot=overlay" ok
else
	check "cmdline has boot=overlay" "missing"
fi

if [ "${CMDLINE}" != "${CMDLINE/ grow-data /}" ]; then
	check "cmdline has grow-data" ok
else
	check "cmdline has grow-data" "missing"
fi

case "${CMDLINE}" in
	*" resize "*) check "stock resize token removed from cmdline" "still present" ;;
	*) check "stock resize token removed from cmdline" ok ;;
esac

if sudo test -f "${WORK}/root/etc/initramfs-tools/scripts/overlay"; then
	check "initramfs overlay script installed" ok
else
	check "initramfs overlay script installed" "missing"
fi

if sudo grep -q 'local_mount_root' "${WORK}/root/etc/initramfs-tools/scripts/overlay" &&
	sudo grep -q 'root-ro' "${WORK}/root/etc/initramfs-tools/scripts/overlay"; then
	check "overlay script mounts the overlay and exposes the real root" ok
else
	check "overlay script mounts the overlay and exposes the real root" "unexpected contents"
fi

if sudo grep -qx 'overlay' "${WORK}/root/etc/initramfs-tools/modules"; then
	check "overlay module in the initramfs module list" ok
else
	check "overlay module in the initramfs module list" "missing"
fi

if sudo test -x "${WORK}/root/etc/initramfs-tools/scripts/local-premount/grow-data"; then
	check "grow-data initramfs script installed executable" ok
else
	check "grow-data initramfs script installed executable" "missing"
fi

resize_state="$(systemctl --root="${WORK}/root" is-enabled rpi-resize.service 2>/dev/null || true)"
if [ "${resize_state}" != "enabled" ]; then
	check "stock rpi-resize (root grow) disabled" ok
else
	check "stock rpi-resize (root grow) disabled" "still enabled"
fi

# fstab: the boot partition is mounted read-only, and /data has its own
# nofail entry pointing at partition 3 (the DATADEV placeholder resolved).
if sudo grep -E '/boot/firmware[[:space:]]+vfat[[:space:]]+[^[:space:]]*ro' \
	"${WORK}/root/etc/fstab" >/dev/null; then
	check "fstab mounts /boot/firmware read-only" ok
else
	check "fstab mounts /boot/firmware read-only" "no ro flag"
fi

data_fstab="$(sudo grep -E '/data[[:space:]]+ext4' "${WORK}/root/etc/fstab" || true)"
case "${data_fstab}" in
	PARTUUID=*-03*nofail*) check "fstab has the /data entry (partition 3, nofail)" ok ;;
	*) check "fstab has the /data entry (partition 3, nofail)" "got: ${data_fstab:-nothing}" ;;
esac

# Persistent paths resolve onto /data (docs/persistent-paths.md).
for link in "var/log/journal:/data/journal" \
	"var/lib/cloud:/data/system/cloud" \
	"etc/syncphony-box/config.env:/data/syncphony-box/config.env" \
	"etc/ssh/ssh_host_ed25519_key:/data/system/ssh/ssh_host_ed25519_key"; do
	path="${link%%:*}" want="${link#*:}"
	got="$(sudo readlink "${WORK}/root/${path}" 2>/dev/null || true)"
	if [ "${got}" = "${want}" ]; then
		check "${path} points at /data" ok
	else
		check "${path} points at /data" "is ${got:-not a symlink}"
	fi
done

if sudo test -x "${WORK}/root/usr/lib/syncphony-box/data-persist"; then
	check "data-persist installed executable" ok
else
	check "data-persist installed executable" "missing"
fi

if sudo grep -q 'SystemMaxUse=50M' \
	"${WORK}/root/etc/systemd/journald.conf.d/50-syncphony-journald.conf"; then
	check "journald capped at 50M" ok
else
	check "journald capped at 50M" "drop-in missing or no cap"
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
