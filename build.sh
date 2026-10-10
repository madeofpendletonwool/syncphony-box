#!/usr/bin/env bash
# Build the syncphony-box image by wrapping pi-gen's build-docker.sh.
#
# What this adds on top of calling pi-gen directly:
#   - resolves the image version from `git describe` and pins the output name
#     to syncphony-box-<version>-arm64.img.xz (+ .sha256)
#   - neutralizes stage2's EXPORT_IMAGE (pi-gen's documented SKIP_IMAGES
#     mechanism) so only stage-syncphony exports an image
#   - bind-mounts stage-syncphony into the build container at /stage-syncphony
#     so the ../stage-syncphony entry of STAGE_LIST resolves (build-docker.sh
#     bakes only the pi-gen submodule into the container image)
#   - drops the result in ./deploy
#
# Environment passthrough (same meaning as pi-gen): CONTINUE, PRESERVE_CONTAINER, DOCKER.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${REPO_ROOT}"

if [ ! -f pi-gen/build-docker.sh ]; then
	echo "pi-gen submodule is not checked out. Run: git submodule update --init" >&2
	exit 1
fi

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo unversioned)"
IMG_BASE="syncphony-box-${VERSION}-arm64"

# Build boxd (static, arm64 — Go cross-compiles) for the custom stage to
# install. Vendored dependencies, so this needs no network.
if ! command -v go >/dev/null 2>&1; then
	echo "Go is required to build boxd (https://go.dev/dl/)." >&2
	exit 1
fi
echo "Building boxd ${VERSION} for arm64..."
( cd boxd &&
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -trimpath \
		-ldflags "-s -w -X main.version=${VERSION}" \
		-o "../stage-syncphony/05-boxd/files/boxd" ./cmd/boxd )

# Only stage-syncphony/EXPORT_IMAGE (empty IMG_SUFFIX) exports an image.
touch pi-gen/stage2/SKIP_IMAGES

mkdir -p .build deploy
{
	cat config
	# Append after the sourced config so these always win. pi-gen names xz
	# output from ARCHIVE_FILENAME and the raw image from IMG_FILENAME.
	printf 'IMG_FILENAME="%s"\n' "${IMG_BASE}"
	printf 'ARCHIVE_FILENAME="%s"\n' "${IMG_BASE}"
} > .build/config

PIGEN_DOCKER_OPTS="--volume ${REPO_ROOT}/stage-syncphony:/stage-syncphony" \
	./pi-gen/build-docker.sh -c "${REPO_ROOT}/.build/config"

IMAGE="deploy/${IMG_BASE}.img.xz"
if [ ! -f "${IMAGE}" ]; then
	echo "Build finished but ${IMAGE} was not produced. deploy/ contains:" >&2
	ls -lah deploy >&2
	exit 1
fi

sha256sum "${IMAGE}" > "${IMAGE}.sha256"

echo
echo "Built ${IMAGE}"
ls -lah deploy
