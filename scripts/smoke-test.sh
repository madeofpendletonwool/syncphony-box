#!/usr/bin/env bash
# Smoke-test a built syncphony-box image without hardware.
#
# Usage: scripts/smoke-test.sh deploy/syncphony-box-<version>-arm64.img.xz
#
# Loop-mounts the image's two partitions and checks that the kiosk packages
# are installed, the cloud-init first-boot machinery (which applies Raspberry
# Pi Imager settings) is enabled, the first user is locked, and the NoCloud
# seed files are present on the boot partition.
#
# Later issues extend this test as the image grows (kiosk units, boxd, ...).

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
for pkg in cage chromium fonts-noto-color-emoji cloud-init; do
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
for unit in cloud-config.service cloud-final.service; do
	state="$(systemctl --root="${WORK}/root" is-enabled "${unit}" 2>/dev/null || true)"
	if [ "${state}" = "enabled" ]; then
		check "unit ${unit} enabled" ok
	else
		check "unit ${unit} enabled" "${state:-not-found}"
	fi
done

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
