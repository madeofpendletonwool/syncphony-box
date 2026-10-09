# ADR 0001: pi-gen image with a cage + Chromium kiosk

- Status: Accepted
- Date: 2026-10-08
- Deciders: Phase 11 plan (MAD-797), box repo groundwork (MAD-798)

## Context

Syncphony Box is a flashable Raspberry Pi image that turns a Pi plugged into a
TV into the room's big screen and its speaker. It must be reproducible from
source in CI, boot on a Pi 4 or Pi 5 to a working kiosk, and behave like an
appliance: flash with Raspberry Pi Imager, plug in, pair once, done.

## Decisions

### Built with pi-gen, pinned to a release tag

The image is built with [RPi-Distro/pi-gen](https://github.com/RPi-Distro/pi-gen)
— the Raspberry Pi Foundation's own image builder — with a custom
`stage-syncphony` stage layered on top of stage2 (Lite). pi-gen is vendored as
a git submodule and is never edited; it is pinned to
**`2026-10-06-raspios-trixie-arm64`**, a release tag of pi-gen's `arm64`
branch corresponding to the current Raspberry Pi OS (Trixie) release.

To bump the pin: `git -C pi-gen checkout <new tag> && git add pi-gen`, and
update the tag recorded here.

Consequences:

- We track Raspberry Pi OS releases cheaply but must re-verify the custom
  stage on every bump.
- Only `stage-syncphony/`, `config`, and our wrapper scripts are ours to
  maintain.

Alternatives rejected: hand-rolled disk-image scripts (fragile, no Foundation
support), `mkosi`/`debos` (not Pi-specific), prebuilt Lite image + overlay
post-processing (not reproducible from source).

### cage + Chromium, no desktop

The kiosk session is the cage Wayland kiosk compositor running Chromium as its
only fullscreen client — not a full desktop environment. The session itself
(unit, wrapper, flags) lands with MAD-799; this repo's first change lays down
the build and the package set (`cage`, `chromium`, `fonts-noto-color-emoji`).

### Client only

The box runs no Syncphony server. It is a kiosk for an existing Syncphony
server, selected at setup. An all-in-one box is out of scope (Phase 11
parent).

### Browser/box communication on loopback only

The web app and the box's helper daemon (`boxd`, later stages) talk only over
the box bridge on `http://127.0.0.1:8099`. A page served over HTTPS may call
`http://127.0.0.1` because loopback counts as a secure origin. The contract is
versioned and documented in Syncphony ADR 0016.

### Read-only root later

The root filesystem will eventually be read-only with an overlay, with
persistent state on a `/data` partition. That is deliberately deferred to the
hardening stage (MAD-805..808) so the early image stays debuggable.

### arm64 only, Pi 4 and Pi 5

Pi OS arm64, no armhf, no Pi 3 / Zero 2 W: the visuals need the Pi 4/5 GPU
class. Requires 2 GB+ RAM.

## Build details

### Stage layout and image export

`STAGE_LIST="stage0 stage1 stage2 ../stage-syncphony"` — the custom stage
lives outside the submodule. Two consequences handled by `./build.sh`:

- pi-gen's `build-docker.sh` bakes only the pi-gen directory into its build
  container, so `../stage-syncphony` would not resolve inside it. The wrapper
  bind-mounts our stage at `/stage-syncphony` via `PIGEN_DOCKER_OPTS`.
- stage2 ships an `EXPORT_IMAGE`, which would export an intermediate `-lite`
  image. The wrapper drops a `SKIP_IMAGES` file into `pi-gen/stage2/` —
  pi-gen's documented mechanism for suppressing a stage's export — so only
  `stage-syncphony/EXPORT_IMAGE` (empty `IMG_SUFFIX`) exports. The file is
  untracked inside the submodule; the submodule itself stays pristine.

### Image naming

Output is `deploy/syncphony-box-<version>-arm64.img.xz` plus a `.sha256`.
`<version>` is `git describe --tags --always --dirty` of this repo, baked into
pi-gen's `IMG_FILENAME`/`ARCHIVE_FILENAME` by the wrapper (pi-gen's xz export
takes its name from `ARCHIVE_FILENAME`).

### First boot: cloud-init (NoCloud) applies Imager settings

Verified against the stock `2026-10-06-raspios-trixie-arm64-lite` image:
this Raspberry Pi OS release applies Raspberry Pi Imager customization (user,
password, Wi-Fi, SSH, hostname) through **cloud-init** with the NoCloud
datasource seeded from `/boot/firmware` (`user-data`, `network-config`,
`meta-data`; `rpi-cloud-init-mods` points cloud-init at that directory). We
therefore keep `ENABLE_CLOUD_INIT=1` and leave the seed files alone; the
smoke test asserts they exist and that `cloud-config.service` and
`cloud-final.service` are enabled.

The first user (`FIRST_USER_NAME=syncphony`) is created by pi-gen with
`adduser --disabled-login` and `FIRST_USER_PASS` deliberately unset, so the
account ships with a locked password: **no default password, login disabled
until the Imager settings apply**. On first boot cloud-init renames this user
and sets the real password from the Imager's `user-data`. Flashed without any
customization, the image behaves like stock Pi OS Lite (the first-boot
`userconfig` dialog on tty8 asks for a username and password).

### CI

GitHub Actions builds natively on `ubuntu-24.04-arm` runners on every push to
`main` and on PRs, uploads the image as a workflow artifact, and runs
`scripts/smoke-test.sh`: loop-mount both partitions of the built image and
check installed packages, enabled units, the locked first user, and the
cloud-init seed files. No hardware in CI; on-Pi verification stays manual.

pi-gen's apt downloads are not cached between CI runs: pi-gen keeps no
persistent apt archive (each stage's rootfs drops `/var/cache/apt/archives`
on `copy_previous`), so there is no supported cache hook. If that changes in
a future pi-gen tag, revisit.
