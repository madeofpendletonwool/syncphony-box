# syncphony-box

A flashable Raspberry Pi image that turns a Pi plugged into a TV into the
room's big screen and its speaker — a kiosk for an existing
[Syncphony](https://github.com/madeofpendletonwool/Syncphony) server. Flash
the image with [Raspberry Pi Imager](https://www.raspberrypi.com/software/),
plug the Pi into the TV, and it boots straight into the room.

The box is **client only**: it runs no Syncphony server. Point it at yours.

## Supported hardware

- Raspberry Pi 4 or Pi 5, 2 GB RAM or more
- 16 GB+ SD card
- arm64 only. Pi 3, Pi Zero 2 W and armhf are not supported.

## Building

Requires a Linux machine with Docker and git. The build runs pi-gen inside a
container; on an arm64 host the rootfs builds natively, on x86 it runs through
QEMU emulation inside the container (much slower — CI is the recommended way
to build).

```sh
git clone --recurse-submodules https://github.com/madeofpendletonwool/syncphony-box
cd syncphony-box
make image
```

The result lands in `deploy/`:

- `syncphony-box-<version>-arm64.img.xz` — the image (`<version>` from
  `git describe`)
- `syncphony-box-<version>-arm64.img.xz.sha256` — its checksum

`make clean` removes build output and the leftover pi-gen build container.

### Continuous integration

GitHub Actions builds the image natively on arm64 runners
(`ubuntu-24.04-arm`) on every push to `main` and on every PR, runs the
no-hardware smoke test (`scripts/smoke-test.sh` loop-mounts the built image
and checks packages, enabled units, the locked first user, the first-boot
seed files, and the kiosk session), and uploads the image as a workflow
artifact.

## The kiosk session

On boot the box starts `syncphony-kiosk.service`: the unprivileged `kiosk`
user runs [cage](https://github.com/cage-kiosk/cage) (a Wayland kiosk
compositor) on tty1, and cage runs Chromium fullscreen. There is no desktop
and no login. If Chromium is killed, the unit restarts it after 2 seconds.
The boot is silent (`quiet loglevel=3`, no rainbow splash, no console
cursor, no console blanking). See [docs/adr/0002-kiosk-session.md](docs/adr/0002-kiosk-session.md).

Every Chromium flag lives in one wrapper, `/usr/lib/syncphony-box/kiosk`,
on the box — including the GPU flags that let Chromium use the Pi's GPU.
The browser opens the server's `/tv` page.

Until the boot config file (`syncphony.txt`) lands, the server URL comes
from `/etc/syncphony-box/url` on the box (one line, e.g.
`https://syncphony.example.com`). Without it the box shows a local "no
server set" page:

```sh
echo https://syncphony.example.com | sudo tee /etc/syncphony-box/url
sudo systemctl restart syncphony-kiosk
```

### On-Pi checklist

CI can't boot a Pi; verify these on hardware when one is at hand:

- Boots to the server's `/tv` fullscreen in under ~30 s on a Pi 5.
- `sudo systemctl kill --kill-who=all syncphony-kiosk` (or killing
  Chromium) brings the session back within a few seconds.
- No visible cursor — including with a mouse plugged in and wiggled.
- No crash bubble, and no "restore pages" prompt after pulling power.
- The `/tv` visualizer runs smoothly with GPU rasterization (check
  `chrome://gpu` on the box); if not, tune the GPU flags in the wrapper.


## Flashing

1. Write the `.img.xz` to an SD card with Raspberry Pi Imager.
2. In the OS customization settings, set at minimum a username and password
   (Wi-Fi, SSH and hostname as needed). The first user in the image ships with
   a **locked password** — there is no default login; the Imager settings are
   applied on first boot by cloud-init.
3. Boot the Pi. Without Imager customization the image behaves like stock
   Raspberry Pi OS Lite and asks for a username and password on the console.

## Repo layout

```
pi-gen/               pi-gen submodule (pinned, never edited)
stage-syncphony/      custom pi-gen stage (packages, kiosk session, ...)
config                pi-gen build configuration
build.sh              wraps pi-gen's build-docker.sh
Makefile              make image / make clean
scripts/smoke-test.sh no-hardware image checks (used by CI)
docs/adr/             architecture decision records
```

## License

AGPL-3.0, matching Syncphony. See [LICENSE](LICENSE).
