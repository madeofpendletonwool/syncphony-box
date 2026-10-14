#!/usr/bin/env bash
# add-data-partition — the /data post-export step (MAD-805, ADR 0007).
#
# pi-gen's export stage lays out two partitions (boot FAT + root ext4) and
# ships the compressed image; nothing inside it can add a third. So after
# pi-gen finishes, this script reopens the built image and appends the
# data partition:
#
#   - a 512 MiB ext4 labelled syncphony-data as partition 3 (the grow-data
#     initramfs script expands it to fill the card on first boot, and
#     syncphony-data-grow.service grows the filesystem to match)
#   - the /data directory layout pre-created, with the Chromium profile
#     dir owned by the kiosk user (docs/persistent-paths.md)
#   - the fstab DATADEV placeholder (written by stage 06) replaced with
#     the new partition's PARTUUID — same disk-id spelling pi-gen's own
#     04-set-partuuid uses for partitions 1 and 2
#
# Needs root for losetup/mount/mke2fs — via sudo, exactly like
# scripts/smoke-test.sh. Runs on the host after ./pi-gen/build-docker.sh;
# ./build.sh calls it.
#
# SB_DATA_PART_SIZE overrides the initial partition size (bytes) for
# tests.

set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <image>.img.xz" >&2
	exit 2
fi

ALIGN=$((8 * 1024 * 1024)) # pi-gen aligns all partition starts to 8 MiB
DATA_SIZE="${SB_DATA_PART_SIZE:-$((512 * 1024 * 1024))}"

IMG_XZ="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
WORK="$(mktemp -d /tmp/syncphony-box-data.XXXXXX)"
LOOP=""

cleanup() {
	sudo umount "${WORK}/root" 2>/dev/null || true
	if [ -n "${LOOP}" ]; then
		sudo losetup -d "${LOOP}" 2>/dev/null || true
	fi
	rm -rf "${WORK}"
}
trap cleanup EXIT

echo "add-data-partition: decompressing ${IMG_XZ} ..."
xz -dk -T0 -c "${IMG_XZ}" > "${WORK}/image.img"
IMG="${WORK}/image.img"

# The MBR disk id is what names partitions in fstab as PARTUUID=<id>-NN.
IMGID="$(dd if="${IMG}" skip=440 bs=1 count=4 2>/dev/null | xxd -e | cut -f 2 -d ' ')"
if [ -z "${IMGID}" ]; then
	echo "add-data-partition: cannot read the disk id" >&2
	exit 1
fi

# Append partition 3 directly after the last existing one, aligned.
LAST_END="$(parted -m -s "${IMG}" unit B print | awk -F: '$1 ~ /^[0-9]+$/ { e = $3 } END { sub(/B$/, "", e); print e }')"
if [ -z "${LAST_END}" ]; then
	echo "add-data-partition: cannot read the partition table" >&2
	exit 1
fi
DATA_START=$(( (LAST_END + 1 + ALIGN - 1) / ALIGN * ALIGN ))
NEW_SIZE=$(( DATA_START + (DATA_SIZE + ALIGN - 1) / ALIGN * ALIGN ))

truncate -s "${NEW_SIZE}" "${IMG}"
parted --script "${IMG}" unit B mkpart primary ext4 "${DATA_START}" "$((NEW_SIZE - 1))"

LOOP="$(sudo losetup --find --show --partscan "${IMG}")"

# The kiosk user's numeric uid/gid from the image's own account database,
# so the pre-created profile dir lands owned correctly even though the
# user only exists inside the image.
mkdir -p "${WORK}/root"
sudo mount -o ro "${LOOP}p2" "${WORK}/root"
KIOSK_UID="$(sudo awk -F: '$1=="kiosk"{print $3}' "${WORK}/root/etc/passwd")"
KIOSK_GID="$(sudo awk -F: '$1=="kiosk"{print $3}' "${WORK}/root/etc/group")"
sudo umount "${WORK}/root"
if [ -z "${KIOSK_UID}" ] || [ -z "${KIOSK_GID}" ]; then
	echo "add-data-partition: no kiosk user in the image" >&2
	exit 1
fi

# The persistent layout (docs/persistent-paths.md). data-persist recreates
# anything missing at boot; pre-creating it makes the very first boot
# (journald, ssh-keygen) persistent from the start.
STAGING="${WORK}/data"
mkdir -p \
	"${STAGING}/chromium" \
	"${STAGING}/journal" \
	"${STAGING}/syncphony-box" \
	"${STAGING}/boxd" \
	"${STAGING}/system/cloud" \
	"${STAGING}/system/identity" \
	"${STAGING}/system/ssh" \
	"${STAGING}/system/network"
sudo chown "${KIOSK_UID}:${KIOSK_GID}" "${STAGING}/chromium"

echo "add-data-partition: making the ${DATA_SIZE}-byte ext4 (label syncphony-data) ..."
sudo mke2fs -q -t ext4 -L syncphony-data -d "${STAGING}" "${LOOP}p3"

# fstab: the DATADEV placeholder (stage 06 wrote it) becomes the real
# PARTUUID, like pi-gen's 04-set-partuuid does for BOOTDEV/ROOTDEV.
sudo mount -o rw "${LOOP}p2" "${WORK}/root"
sudo sed -i "s/DATADEV/PARTUUID=${IMGID}-03/" "${WORK}/root/etc/fstab"
sudo umount "${WORK}/root"

sudo losetup -d "${LOOP}"
LOOP=""

echo "add-data-partition: recompressing ..."
xz --compress -T0 --memlimit-compress=50% -6 -c "${IMG}" > "${WORK}/out.img.xz"
mv "${WORK}/out.img.xz" "${IMG_XZ}"

echo "add-data-partition: done — ${IMG_XZ} now has three partitions."
