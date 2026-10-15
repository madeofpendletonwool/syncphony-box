#!/usr/bin/env bash
# Smoke-test a built syncphony-box image without hardware.
#
# Usage: scripts/smoke-test.sh deploy/syncphony-box-<version>-arm64.img.xz
#        scripts/smoke-test.sh deploy/syncphony-box-<version>-amd64.iso
#
# The x86 ISO (MAD-846, ADR 0013) gets the same rootfs checks, run against
# its squashfs: both images come out of the same customise layer
# (rootfs/), so they must agree. The Pi-only checks (the three partitions,
# cloud-init and the Imager seed, config.txt, vcgencmd) are skipped there,
# and the ISO gets its own: both boot menus, the EFI and BIOS loaders, the
# overlay inside the initramfs, the installer and the live-session units.
#
# For the Pi image: loop-mounts the image's three partitions and checks that the kiosk packages
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
# /dev/uinput, boxd granted the group, and uinput loaded at boot. And the
# Wi-Fi setup hotspot's: NetworkManager and dnsmasq, the captive-portal
# DNS catch-all, and boxd ordered after NetworkManager. And the release
# and update pieces (MAD-809, MAD-810): the box's files come from the
# syncphony-box package at this checkout's version, the nightly update
# timer is on, the box repo is the updater's alone (never in /etc/apt),
# docs and man pages are stripped, and the image is under 1 GiB.

set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <image>.img.xz | <image>.iso" >&2
	exit 2
fi

IMAGE_XZ="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
case "${IMAGE_XZ}" in
	*.img.xz) PLATFORM=pi ;;
	*.iso) PLATFORM=x86 ;;
	*)
		echo "$0: want a .img.xz (Pi) or an .iso (x86), got $1" >&2
		exit 2
		;;
esac
WORK="$(mktemp -d /tmp/syncphony-box-smoke.XXXXXX)"
LOOP=""

cleanup() {
	sudo umount "${WORK}/boot" "${WORK}/root" "${WORK}/data" 2>/dev/null || true
	sudo umount "${WORK}/iso" 2>/dev/null || true
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

# has_tokens LINE TOKEN...: every token is a whole word of LINE.
has_tokens() {
	local line=" $1 " tok
	shift
	for tok in "$@"; do
		case "${line}" in *" ${tok} "*) ;; *) return 1 ;; esac
	done
}

mkdir -p "${WORK}/boot" "${WORK}/root" "${WORK}/data" "${WORK}/iso"
if [ "${PLATFORM}" = pi ]; then
	echo "Decompressing ${IMAGE_XZ} ..."
	xz -dk -T0 -c "${IMAGE_XZ}" > "${WORK}/image.img"

	LOOP="$(sudo losetup --find --show --partscan "${WORK}/image.img")"
	sudo mount -o ro "${LOOP}p1" "${WORK}/boot"
	sudo mount -o ro "${LOOP}p2" "${WORK}/root"
	sudo mount -o ro "${LOOP}p3" "${WORK}/data"
	# The shipped syncphony.txt: on the boot partition.
	TXT="${WORK}/boot/syncphony.txt"
else
	sudo mount -o loop,ro "${IMAGE_XZ}" "${WORK}/iso"
	sudo mount -t squashfs -o loop,ro "${WORK}/iso/live/filesystem.squashfs" "${WORK}/root"
	# The installer and the live session copy the packaged template.
	TXT="${WORK}/root/usr/lib/syncphony-box/syncphony.txt"
fi

if [ "${PLATFORM}" = pi ]; then
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
fi # pi

# --- packages ---------------------------------------------------------------
if [ "${PLATFORM}" = pi ]; then
	platform_pkgs="cloud-init raspi-utils-core"
	platform_units="cloud-config.service cloud-final.service"
else
	platform_pkgs="linux-image-amd64 live-boot libpam-systemd grub-pc-bin grub-efi-amd64-bin \
		fdisk dosfstools whiptail firmware-misc-nonfree firmware-sof-signed alsa-ucm-conf"
	platform_units="syncphony-live-boot.service syncphony-installer.service"
fi
# shellcheck disable=SC2086 # the platform lists split on purpose
for pkg in cage chromium fonts-noto-color-emoji libgl1-mesa-dri avahi-daemon \
	pipewire pipewire-bin pipewire-alsa pipewire-pulse wireplumber pulseaudio-utils alsa-utils \
	systemd-timesyncd v4l-utils network-manager dnsmasq-base syncphony-box ${platform_pkgs}; do
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
# shellcheck disable=SC2086
for unit in syncphony-kiosk.service syncphony-box-config.service \
	avahi-daemon.service syncphony-audio.service syncphony-audio-monitor.service boxd.service \
	syncphony-data-grow.service syncphony-data-prepare.service syncphony-data-save.service \
	syncphony-data-save.timer systemd-timesyncd.service syncphony-update.timer ${platform_units}; do
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

# The image's boxd is a static binary for the image's architecture, and CI
# smoke-tests each image on a runner of that architecture (arm64 for the
# Pi, x86 for the ISO), so it can run directly; where it can't (an image
# for the other architecture), skip the run-time checks and keep the
# static ones.
if out="$(sudo "${WORK}/root/usr/lib/syncphony-box/boxd" profile path 2>/dev/null)" &&
	[ -n "${out}" ]; then
	profile_dir="${out}"
	check "boxd profile path answers" ok
else
	profile_dir="/data/chromium"
	check "boxd profile path answers" "skipped (cannot run the image's binary here)"
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
	if out="$(sudo "${WORK}/root/usr/lib/syncphony-box/boxd" parse "${TXT}" 2>/dev/null)" &&
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

# Static (CGO_ENABLED=0) binary for the image's architecture.
if [ "${PLATFORM}" = pi ]; then
	boxd_arch='ARM aarch64'
else
	boxd_arch='x86-64'
fi
if ! command -v file >/dev/null 2>&1; then
	check "boxd is a static binary" ok
elif file "${WORK}/root/usr/lib/syncphony-box/boxd" 2>/dev/null |
	grep -q "ELF 64-bit LSB.*${boxd_arch}.*statically linked"; then
	check "boxd is a static binary" ok
else
	check "boxd is a static binary" "$(file "${WORK}/root/usr/lib/syncphony-box/boxd" 2>/dev/null)"
fi

if sudo grep -q '^ExecStart=/usr/lib/syncphony-box/boxd serve$' \
	"${WORK}/root/usr/lib/systemd/system/boxd.service"; then
	check "boxd.service runs boxd serve" ok
else
	check "boxd.service runs boxd serve" "ExecStart mismatch"
fi

# --- watchdogs and self-healing (MAD-806) -------------------------------------
if sudo grep -q '^RuntimeWatchdogSec=15s' \
	"${WORK}/root/usr/lib/systemd/system.conf.d/50-syncphony-watchdog.conf"; then
	check "hardware watchdog armed at 15 s" ok
else
	check "hardware watchdog armed at 15 s" "drop-in missing or wrong"
fi

if sudo grep -q '^RebootWatchdogSec=' \
	"${WORK}/root/usr/lib/systemd/system.conf.d/50-syncphony-watchdog.conf"; then
	check "reboot watchdog bounded" ok
else
	check "reboot watchdog bounded" "no RebootWatchdogSec"
fi

if sudo grep -q 'time-sync.target' "${WORK}/root/usr/lib/systemd/system/syncphony-kiosk.service"; then
	check "kiosk unit waits for time sync" ok
else
	check "kiosk unit waits for time sync" "no time-sync.target ordering"
fi

if [ "${PLATFORM}" = pi ]; then
	if sudo test -x "${WORK}/root/usr/bin/vcgencmd"; then
		check "vcgencmd installed (thermals)" ok
	else
		check "vcgencmd installed (thermals)" "missing or not executable"
	fi
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
	"${WORK}/root/usr/lib/udev/rules.d/60-syncphony-cec.rules" &&
	sudo grep -q 'KERNEL=="cec\[0-9\]\*", GROUP="cec"' \
	"${WORK}/root/usr/lib/udev/rules.d/60-syncphony-cec.rules"; then
	check "udev rule grants cec and uinput to the group" ok
else
	check "udev rule grants cec and uinput to the group" "rule missing or wrong"
fi

if sudo grep -q '^SupplementaryGroups=cec' \
	"${WORK}/root/usr/lib/systemd/system/boxd.service.d/50-cec.conf"; then
	check "boxd holds the cec group" ok
else
	check "boxd holds the cec group" "drop-in missing or wrong"
fi

if sudo grep -q '^uinput$' "${WORK}/root/usr/lib/modules-load.d/syncphony-cec.conf"; then
	check "uinput loaded at boot" ok
else
	check "uinput loaded at boot" "modules-load entry missing"
fi

# --- Wi-Fi setup hotspot (MAD-808) --------------------------------------------
if sudo test -x "${WORK}/root/usr/bin/nmcli" &&
	sudo test -x "${WORK}/root/usr/sbin/dnsmasq"; then
	check "NetworkManager and dnsmasq installed (hotspot)" ok
else
	check "NetworkManager and dnsmasq installed (hotspot)" "nmcli or dnsmasq missing"
fi

if sudo grep -q '^address=/#/10.42.0.1$' \
	"${WORK}/root/etc/NetworkManager/dnsmasq-shared.d/syncphony-captive.conf"; then
	check "hotspot DNS catch-all points at boxd" ok
else
	check "hotspot DNS catch-all points at boxd" "dnsmasq-shared.d drop-in missing or wrong"
fi

if sudo grep -q '^After=NetworkManager.service' \
	"${WORK}/root/usr/lib/systemd/system/boxd.service.d/60-network.conf"; then
	check "boxd starts after NetworkManager" ok
else
	check "boxd starts after NetworkManager" "drop-in missing or wrong"
fi

if sudo test -L "${WORK}/root/etc/systemd/system/multi-user.target.wants/NetworkManager.service"; then
	check "NetworkManager enabled" ok
else
	check "NetworkManager enabled" "not enabled"
fi

# The boot config apply is boxd now: same unit, new engine.
if sudo grep -q '^ExecStart=/usr/lib/syncphony-box/boxd config apply$' \
	"${WORK}/root/usr/lib/systemd/system/syncphony-box-config.service"; then
	check "config apply runs boxd" ok
else
	check "config apply runs boxd" "ExecStart mismatch"
fi

if sudo grep -q '127.0.0.1:8099' "${WORK}/root/usr/lib/syncphony-box/kiosk"; then
	check "kiosk wrapper enters through boxd" ok
else
	check "kiosk wrapper enters through boxd" "no boxd URL in the wrapper"
fi

if sudo grep -q 'boxd.service' "${WORK}/root/usr/lib/systemd/system/syncphony-kiosk.service"; then
	check "kiosk unit waits for boxd" ok
else
	check "kiosk unit waits for boxd" "no boxd reference"
fi

if [ -f "${TXT}" ]; then
	check "syncphony.txt ships (${TXT#"${WORK}"/})" ok
else
	check "syncphony.txt ships (${TXT#"${WORK}"/})" "missing"
fi

# The file must ship fully commented out: every non-comment line is empty.
active="$(sudo grep -Ev '^[[:space:]]*(#|$)' "${TXT}" 2>/dev/null || true)"
if [ -z "${active}" ]; then
	check "syncphony.txt ships fully commented out" ok
else
	check "syncphony.txt ships fully commented out" "active lines: ${active}"
fi

for key in server_url name audio resolution rotate cec cec_standby cec_standby_minutes auto_update; do
	if sudo grep -q "^#${key}=" "${TXT}"; then
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

# A link into the package (absolute, so read the target inside the image).
wp_link="$(sudo readlink "${WORK}/root/var/lib/syncphony-box/.config/wireplumber/wireplumber.conf.d/50-syncphony-box.conf" 2>/dev/null || true)"
if [ "${wp_link}" = "/usr/share/syncphony-box/wireplumber/50-syncphony-box.conf" ] &&
	sudo grep -q 'session.suspend-timeout-seconds = 0' "${WORK}/root${wp_link}"; then
	check "wireplumber keeps outputs unsuspended" ok
else
	check "wireplumber keeps outputs unsuspended" "conf missing or rule absent"
fi

if sudo grep -q 'syncphony-audio.service' "${WORK}/root/usr/lib/systemd/system/syncphony-kiosk.service"; then
	check "kiosk waits for the audio unit" ok
else
	check "kiosk waits for the audio unit" "no reference in syncphony-kiosk.service"
fi

# --- boot presentation ------------------------------------------------------
# The installed box's kernel command line: the Pi's cmdline.txt, or on x86
# the cmdline.cfg the installer puts on the EFI partition (boxd rewrites
# both; ADR 0013).
if [ "${PLATFORM}" = pi ]; then
	CMDLINE=" $(sudo cat "${WORK}/boot/cmdline.txt") "
else
	CMDLINE=" $(sudo sed -n 's/^set syncphony_cmdline="\(.*\)"$/\1/p' \
		"${WORK}/root/usr/share/syncphony-box/x86/cmdline.cfg" 2>/dev/null) "
fi
for param in quiet splash loglevel=3 vt.global_cursor_default=0 consoleblank=0; do
	if [ "${CMDLINE}" != "${CMDLINE/ ${param} /}" ]; then
		check "cmdline has ${param}" ok
	else
		check "cmdline has ${param}" "missing"
	fi
done

if [ "${PLATFORM}" = pi ]; then
	if sudo grep -q '^disable_splash=1' "${WORK}/boot/config.txt"; then
		check "config.txt has disable_splash=1" ok
	else
		check "config.txt has disable_splash=1" "missing"
	fi
fi

# --- read-only root (MAD-805) ------------------------------------------------
# boot=overlay activates the overlay script in the initramfs; the stock
# `resize` token must be gone (it would grow the read-only root).
if [ "${CMDLINE}" != "${CMDLINE/ boot=overlay /}" ]; then
	check "cmdline has boot=overlay" ok
else
	check "cmdline has boot=overlay" "missing"
fi

# The Pi grows /data on first boot; the x86 installer makes it full size.
if [ "${PLATFORM}" = pi ]; then
	if [ "${CMDLINE}" != "${CMDLINE/ grow-data /}" ]; then
		check "cmdline has grow-data" ok
	else
		check "cmdline has grow-data" "missing"
	fi
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

if sudo grep -q 'copy_exec /usr/sbin/e2fsck' "${WORK}/root/etc/initramfs-tools/hooks/syncphony-box" 2>/dev/null &&
	sudo test -x "${WORK}/root/etc/initramfs-tools/hooks/syncphony-box"; then
	check "initramfs hook carries e2fsck (the /data check)" ok
else
	check "initramfs hook carries e2fsck (the /data check)" "hook missing or wrong"
fi

if sudo grep -q '^ConditionKernelCommandLine=!boot=live$' \
	"${WORK}/root/usr/lib/systemd/system/syncphony-update.service"; then
	check "nightly update stays off in a live session" ok
else
	check "nightly update stays off in a live session" "no condition"
fi

resize_state="$(systemctl --root="${WORK}/root" is-enabled rpi-resize.service 2>/dev/null || true)"
if [ "${resize_state}" != "enabled" ]; then
	check "stock rpi-resize (root grow) disabled" ok
else
	check "stock rpi-resize (root grow) disabled" "still enabled"
fi

# fstab: the boot partition is mounted read-only, and /data has its own
# nofail entry (the DATADEV placeholder resolved): partition 3 on the Pi,
# the label on the x86 live root. The installer writes an installed x86
# box's fstab, so its lines are checked in the installer.
if [ "${PLATFORM}" = pi ]; then
	boot_fstab="${WORK}/root/etc/fstab"
else
	boot_fstab="${WORK}/root/usr/sbin/syncphony-install"
fi
if sudo grep -E '/boot/firmware[[:space:]]+vfat[[:space:]]+[^[:space:]]*ro' \
	"${boot_fstab}" >/dev/null; then
	check "fstab mounts /boot/firmware read-only" ok
else
	check "fstab mounts /boot/firmware read-only" "no ro flag"
fi

data_fstab="$(sudo grep -E '/data[[:space:]]+ext4' "${WORK}/root/etc/fstab" || true)"
case "${PLATFORM}:${data_fstab}" in
	pi:PARTUUID=*-03*nofail*) check "fstab has the /data entry (partition 3, nofail)" ok ;;
	x86:LABEL=syncphony-data*nofail*) check "fstab has the /data entry (by label, nofail)" ok ;;
	*) check "fstab has the /data entry (nofail)" "got: ${data_fstab:-nothing}" ;;
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
	"${WORK}/root/usr/lib/systemd/journald.conf.d/50-syncphony-journald.conf"; then
	check "journald capped at 50M" ok
else
	check "journald capped at 50M" "drop-in missing or no cap"
fi

# --- the syncphony-box package and updates (MAD-810) -------------------------
# The image installs the box's software as a package; the version must be
# the one this checkout builds (not one pulled from the apt repo).
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
want_version="$("${REPO_ROOT}/scripts/version.sh" --deb 2>/dev/null || true)"
got_version="$(sudo awk '
	$0 == "Package: syncphony-box" { inpkg = 1 }
	inpkg && /^Version: / { print $2; exit }
' "${WORK}/root/var/lib/dpkg/status")"
if [ -n "${got_version}" ] && { [ -z "${want_version}" ] || [ "${got_version}" = "${want_version}" ]; }; then
	check "syncphony-box package is this build (${got_version})" ok
else
	check "syncphony-box package is this build" "installed ${got_version:-nothing}, built ${want_version}"
fi

if sudo grep -q '^ExecStart=/usr/lib/syncphony-box/boxd update$' \
	"${WORK}/root/usr/lib/systemd/system/syncphony-update.service" &&
	sudo grep -q '^OnCalendar=\*-\*-\* 04:00:00$' \
	"${WORK}/root/usr/lib/systemd/system/syncphony-update.timer"; then
	check "nightly update runs boxd update at 04:00" ok
else
	check "nightly update runs boxd update at 04:00" "unit or timer wrong"
fi

# The box's repo is the updater's alone: never in sources.list.d, where
# the image build's (and a person's) apt runs would use it.
if sudo grep -rqs 'syncphony-box' "${WORK}/root/etc/apt/sources.list.d/" "${WORK}/root/etc/apt/sources.list"; then
	check "box repo stays out of the system apt sources" "found in /etc/apt"
else
	check "box repo stays out of the system apt sources" ok
fi

if [ -f "${REPO_ROOT}/packaging/apt/syncphony-box-archive-keyring.asc" ]; then
	if sudo test -f "${WORK}/root/usr/share/syncphony-box/apt/syncphony-box.list" &&
		sudo cmp -s "${REPO_ROOT}/packaging/apt/syncphony-box-archive-keyring.asc" \
			"${WORK}/root/usr/share/keyrings/syncphony-box-archive-keyring.asc"; then
		check "updater's apt list and repo key installed" ok
	else
		check "updater's apt list and repo key installed" "list or keyring missing"
	fi
else
	check "updater's apt list and repo key installed" ok
	echo "NOTE: no repo key in this checkout; the image can't update itself (docs/releasing.md)"
fi

# --- slim (MAD-809: under ~1 GB compressed) --------------------------------
if sudo test -f "${WORK}/root/etc/dpkg/dpkg.cfg.d/50-syncphony-slim"; then
	check "dpkg keeps docs and translations out" ok
else
	check "dpkg keeps docs and translations out" "no 50-syncphony-slim"
fi

man_files="$(sudo find "${WORK}/root/usr/share/man" -type f 2>/dev/null | wc -l)"
doc_files="$(sudo find "${WORK}/root/usr/share/doc" -type f ! -name copyright 2>/dev/null | wc -l)"
if [ "${man_files}" -eq 0 ] && [ "${doc_files}" -eq 0 ]; then
	check "man pages and docs stripped (copyright kept)" ok
else
	check "man pages and docs stripped (copyright kept)" "${man_files} man, ${doc_files} doc files left"
fi

# The Pi image under 1 GiB compressed (MAD-809). The ISO carries PC
# firmware the Pi doesn't need; it must stay under 2 GiB, GitHub's
# release asset limit.
image_bytes="$(stat -c %s "${IMAGE_XZ}")"
if [ "${PLATFORM}" = pi ]; then
	limit_gib=1
else
	limit_gib=2
fi
limit=$((limit_gib * 1024 * 1024 * 1024))
if [ "${image_bytes}" -le "${limit}" ]; then
	check "image under ${limit_gib} GiB ($((image_bytes / 1024 / 1024)) MiB)" ok
else
	check "image under ${limit_gib} GiB" "$((image_bytes / 1024 / 1024)) MiB"
fi

if [ "${PLATFORM}" = pi ]; then
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
fi # pi

# --- the x86 ISO (MAD-846, ADR 0013) ------------------------------------------
if [ "${PLATFORM}" = x86 ]; then
	ISO="${WORK}/iso"
	GRUB_CFG="${ISO}/boot/grub/grub.cfg"

	# One menu for BIOS and UEFI: the live box, and the installer.
	live_entry="$(sudo grep -E '^[[:space:]]*linux[[:space:]]' "${GRUB_CFG}" 2>/dev/null | grep -v syncphony.install | head -n 1 || true)"
	if has_tokens "${live_entry}" boot=live quiet loglevel=3 vt.global_cursor_default=0 consoleblank=0; then
		check "ISO menu boots the live box quietly" ok
	else
		check "ISO menu boots the live box quietly" "got: ${live_entry:-no entry}"
	fi
	if sudo grep -qE '^[[:space:]]*linux[[:space:]].*boot=live.*syncphony\.install' "${GRUB_CFG}" 2>/dev/null; then
		check "ISO menu has the Install to disk entry" ok
	else
		check "ISO menu has the Install to disk entry" "missing"
	fi

	if [ -n "$(sudo find "${ISO}" -ipath '*/efi/boot/bootx64.efi' 2>/dev/null | head -n 1)" ]; then
		check "ISO boots on UEFI (EFI/BOOT/BOOTX64.EFI)" ok
	else
		check "ISO boots on UEFI (EFI/BOOT/BOOTX64.EFI)" "missing"
	fi
	if sudo test -d "${ISO}/boot/grub/i386-pc"; then
		check "ISO boots on BIOS (grub i386-pc)" ok
	else
		check "ISO boots on BIOS (grub i386-pc)" "no boot/grub/i386-pc"
	fi

	initrd="$(sudo find "${ISO}/live" -maxdepth 1 -name 'initrd.img*' | head -n 1)"
	if [ -n "${initrd}" ] && sudo test -n "$(sudo find "${ISO}/live" -maxdepth 1 -name 'vmlinuz*')"; then
		check "ISO carries the kernel and initramfs" ok
	else
		check "ISO carries the kernel and initramfs" "missing in live/"
	fi

	# One initramfs serves both: live-boot's scripts for boot=live, the
	# box's overlay (and the e2fsck it runs on /data) for boot=overlay.
	if command -v lsinitramfs >/dev/null 2>&1 && [ -n "${initrd}" ]; then
		contents="$(sudo lsinitramfs "${initrd}" 2>/dev/null || true)"
		for want in scripts/live scripts/overlay e2fsck; do
			# A here-string, not a pipe: grep -q quitting early would
			# SIGPIPE the writer and fail the pipeline under pipefail.
			if grep -q "${want}\$" <<< "${contents}"; then
				check "initramfs has ${want}" ok
			else
				check "initramfs has ${want}" "missing"
			fi
		done
	else
		echo "NOTE: no lsinitramfs here; the initramfs contents go unchecked"
	fi

	if sudo test -x "${WORK}/root/usr/sbin/syncphony-install"; then
		check "installer installed executable" ok
	else
		check "installer installed executable" "missing or not executable"
	fi
	for f in grub.cfg.in cmdline.cfg; do
		if sudo test -f "${WORK}/root/usr/share/syncphony-box/x86/${f}"; then
			check "installer template ${f} present" ok
		else
			check "installer template ${f} present" "missing"
		fi
	done
	if sudo grep -q 'cmdline.cfg' "${WORK}/root/usr/share/syncphony-box/x86/grub.cfg.in" &&
		sudo grep -q '@ROOT_UUID@' "${WORK}/root/usr/share/syncphony-box/x86/grub.cfg.in"; then
		check "installed grub.cfg sources boxd's cmdline.cfg" ok
	else
		check "installed grub.cfg sources boxd's cmdline.cfg" "template wrong"
	fi
	for dir in i386-pc x86_64-efi; do
		if sudo test -d "${WORK}/root/usr/lib/grub/${dir}"; then
			check "GRUB ${dir} modules for the installer" ok
		else
			check "GRUB ${dir} modules for the installer" "missing"
		fi
	done

	# The install boot runs the installer instead of the box, and mounts
	# no /data (the disk being wiped may hold one).
	for dropin in syncphony-kiosk.service.d boxd.service.d data.mount.d; do
		if sudo grep -q '^ConditionKernelCommandLine=!syncphony.install$' \
			"${WORK}/root/usr/lib/systemd/system/${dropin}/50-installer.conf" 2>/dev/null; then
			check "${dropin%.d} stays off in the install boot" ok
		else
			check "${dropin%.d} stays off in the install boot" "drop-in missing or wrong"
		fi
	done

	if [ "$(sudo cat "${WORK}/root/etc/hostname" 2>/dev/null)" = syncphony-box ]; then
		check "hostname is syncphony-box" ok
	else
		check "hostname is syncphony-box" "got: $(sudo cat "${WORK}/root/etc/hostname" 2>/dev/null)"
	fi
	if [ ! -s "${WORK}/root/etc/machine-id" ]; then
		check "machine-id empty (made per box)" ok
	else
		check "machine-id empty (made per box)" "baked in"
	fi
fi

if [ "${failures}" -ne 0 ]; then
	echo "${failures} check(s) failed" >&2
	exit 1
fi

echo "All checks passed."
