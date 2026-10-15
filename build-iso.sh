#!/usr/bin/env bash
# Build the x86-64 live/installer ISO (MAD-846, ADR 0013) with Debian's
# live-build, the way ./build.sh builds the Pi image with pi-gen.
#
#   - resolves the version from `git describe` and names the output
#     syncphony-box-<version>-amd64.iso (+ .sha256)
#   - builds boxd for amd64 and the syncphony-box_<deb-version>_amd64.deb
#     (scripts/build-deb.sh), which lands in deploy/ too, for the apt repo
#   - assembles the live-build tree in .build/live: x86/live (the lb config
#     and hooks), x86/root (the image-only files: installer, units) and the
#     shared customise layer (rootfs/) with the .deb, which the chroot hook
#     runs
#   - runs lb in a privileged Debian Trixie container (live-build mounts
#     /proc, /sys and /dev in its chroot), as linux/amd64: native on an x86
#     host, emulated (slow) on an arm64 one. The chroot is built in the
#     container's own filesystem, not the bind-mounted checkout (a macOS
#     or root-squashed mount can't hold a Debian root); only the ISO and
#     build.log come back out.
#   - drops the result in ./deploy
#
# Environment: DOCKER (default docker), LB_IMAGE (default debian:trixie).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${REPO_ROOT}"

DOCKER="${DOCKER:-docker}"
LB_IMAGE="${LB_IMAGE:-debian:trixie}"
VERSION="$(scripts/version.sh)"
ISO_BASE="syncphony-box-${VERSION}-amd64"

if ! command -v go >/dev/null 2>&1; then
	echo "Go is required to build boxd (https://go.dev/dl/)." >&2
	exit 1
fi
if ! command -v "${DOCKER}" >/dev/null 2>&1; then
	echo "${DOCKER} is required: live-build runs in a Debian Trixie container." >&2
	exit 1
fi

echo "Building boxd ${VERSION} for amd64..."
mkdir -p .build
( cd boxd &&
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath \
		-ldflags "-s -w -X main.version=${VERSION}" \
		-o "../.build/boxd-amd64" ./cmd/boxd )

rm -rf .build/deb-amd64
DEB="$(scripts/build-deb.sh amd64 .build/boxd-amd64 .build/deb-amd64)"
echo "Packaged ${DEB}"

# The live-build config tree (the container copies it in and builds there).
LB=.build/live
rm -rf "${LB}"
cp -a x86/live "${LB}"
INCLUDES="${LB}/config/includes.chroot_after_packages"
mkdir -p "${INCLUDES}/root"
cp -a x86/root/. "${INCLUDES}/"
cp -a rootfs "${INCLUDES}/root/syncphony-rootfs"
cp "${DEB}" "${INCLUDES}/root/syncphony-rootfs/syncphony-box.deb"

echo "Running live-build in ${LB_IMAGE} (linux/amd64)..."
OUT=.build/live-out
rm -rf "${OUT}"
mkdir -p "${OUT}"
"${DOCKER}" run --rm --privileged --platform linux/amd64 \
	-v "${REPO_ROOT}/${LB}:/src:ro" -v "${REPO_ROOT}/${OUT}:/out" \
	-e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
	"${LB_IMAGE}" bash -euc '
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -y -qq --no-install-recommends \
			live-build debootstrap ca-certificates xorriso \
			grub-pc-bin grub-efi-amd64-bin mtools dosfstools squashfs-tools cpio >/dev/null
		cp -a /src /build
		cd /build
		lb config
		set +e
		lb build 2>&1 | tee build.log
		status="${PIPESTATUS[0]}"
		set -e
		cp build.log *.iso /out/ 2>/dev/null || true
		chown -R "${HOST_UID}:${HOST_GID}" /out
		exit "${status}"
	'

mkdir -p deploy
built="$(ls "${OUT}"/*.iso 2>/dev/null | head -n 1 || true)"
if [ -z "${built}" ]; then
	echo "live-build finished but produced no ISO; see ${OUT}/build.log" >&2
	exit 1
fi
ISO="deploy/${ISO_BASE}.iso"
mv "${built}" "${ISO}"
sha256sum "${ISO}" > "${ISO}.sha256"
# The package the ISO carries, for the apt repo (installed x86 boxes
# update from it like the Pis do).
cp "${DEB}" deploy/

echo
echo "Built ${ISO}"
ls -lah deploy
