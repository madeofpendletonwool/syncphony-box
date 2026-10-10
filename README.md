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

`make test` runs the unit tests — the bats suite (`tests/`) that pins the
`syncphony.txt` behavior against a boxd binary built from the repo, plus
`go test` for boxd — no hardware or image build needed; install bats
(`apt install bats`) and Go first. `make lint` checks gofmt and go vet.
CI runs them on every PR and push.

### Continuous integration
GitHub Actions builds the image natively on arm64 runners
(`ubuntu-24.04-arm`) on every push to `main` and on every PR, runs the
bats and Go tests, runs the no-hardware smoke test (`scripts/smoke-test.sh`
loop-mounts the built image and checks packages, enabled units, the locked
first user, the first-boot seed files, the kiosk session, the boot
config file, the audio stack, and boxd), and uploads the image as a workflow
artifact.

## boxd

`boxd` is the box's helper daemon — a small static Go program that owns
everything that isn't Chromium ([ADR 0005](docs/adr/0005-boxd.md)). It
parses `syncphony.txt` on every boot, serves the box bridge on
`http://127.0.0.1:8099` for the `/tv` page (Syncphony
[ADR 0016](https://github.com/madeofpendletonwool/Syncphony/blob/main/docs/adr/0016-syncphony-box.md)),
and serves the box's own screens:

- **Setup.** With no `server_url` set, the TV shows a big QR code. Scan it
  with a phone on the same network (or open `http://<box>.local`), type
  your Syncphony server's address, and Save — boxd checks the server's
  `/healthz`, writes it to `syncphony.txt` (still the single source of
  truth), and restarts the kiosk into your server's pairing screen. That
  listener is on the LAN only while the box is unconfigured (later, or for
  10 minutes after a long OK press once the remote lands in stage 3), and
  it never offers anything beyond the URL and the box's name.
- **Offline.** The kiosk never navigates straight at the server: it opens a
  local start page that checks `/healthz` first. If the server can't be
  reached, the TV shows "Can't reach `<server>` — retrying" with the box's
  IP and a countdown, and goes back to the room by itself once the server
  answers — never Chromium's error page. If the server goes away while the
  room is open, boxd notices (heartbeats keep coming while the page is
  alive) and moves the TV to the offline screen after a couple of minutes.

Logs go to journald: `journalctl -u boxd -u syncphony-kiosk`.

## The kiosk session

On boot the box starts `syncphony-kiosk.service`: the unprivileged `kiosk`
user runs [cage](https://github.com/cage-kiosk/cage) (a Wayland kiosk
compositor) on tty1, and cage runs Chromium fullscreen. There is no desktop
and no login. If Chromium is killed, the unit restarts it after 2 seconds.
The boot is silent (`quiet loglevel=3`, no rainbow splash, no console
cursor, no console blanking). See [docs/adr/0002-kiosk-session.md](docs/adr/0002-kiosk-session.md).

Every Chromium flag lives in one wrapper, `/usr/lib/syncphony-box/kiosk`,
on the box — including the GPU flags that let Chromium use the Pi's GPU.
The browser enters through boxd's local pages and opens the server's `/tv`
from there.

## Audio

The box is the room's speaker: Chromium plays everything through the
`<audio>` element on `/tv`, and the box's audio stack is PipeWire (the
Raspberry Pi OS default) with WirePlumber. It runs as the `kiosk` user's
own services, started at boot via [linger] — no login, no system-mode
daemon. See [docs/adr/0004-audio-output.md](docs/adr/0004-audio-output.md).

[linger]: https://www.freedesktop.org/software/systemd/man/latest/loginctl.html

- `audio=` from `syncphony.txt` picks the sink at every boot
  (`hdmi` = HDMI0, the port next to USB-C power; `hdmi2` = HDMI1;
  `analog` = the Pi 4's 3.5 mm jack; `usb` = the first USB audio device).
- The chosen sink is the default, at **100% and unmuted**. Nothing in the
  box scales the sound down: the TV's remote (HDMI carries PCM) or the
  room's own volume control sets the level.
- The kiosk waits for the audio unit like it waits for the network, so
  Chromium never starts before the audio server is up — and a
  hotplug monitor re-applies the choice when outputs appear or disappear
  (a TV that was off at boot and turns on later, HDMI unplugged and back).
- Outputs never auto-suspend, so the first notes of a song aren't clipped
  by a device wake-up.

To try sound by hand on a box (from a console or SSH): `pactl info` shows
the default sink, and this plays a test tone through it:

```sh
aplay /usr/share/sounds/alsa/Front_Center.wav   # or: pw-play /usr/share/sounds/alsa/Front_Center.wav
```

Changing `audio=` takes effect on the next boot.

## Configuration: syncphony.txt

The box is configured by a file you can edit from any computer. Turn the
box off, put the SD card in a card reader, and open `syncphony.txt` on the
card's boot partition (the small drive, labeled `bootfs`; on the box it is
`/boot/firmware/syncphony.txt`). Remove the leading `# ` from the options
you want, save, put the card back, and power the box on. CRLF line endings
and a BOM are fine — Notepad works. A missing or invalid file never stops
the box from booting; bad values are logged and fall back to defaults.

| option | values | default | what it does |
|---|---|---|---|
| `server_url` | `https://…` | unset | the Syncphony server this box shows |
| `name` | any text | unset | name in the room's Screens list; also the hostname (`Living room TV` → `living-room-tv`, advertised as `living-room-tv.local`) |
| `audio` | `hdmi`, `hdmi2`, `analog`, `usb` | `hdmi` | where the sound goes (`analog` is the Pi 4's jack — a Pi 5 has none; use `usb` for a DAC) |
| `resolution` | e.g. `1920x1080@60` | auto | forces the HDMI mode (takes a reboot; the box reboots once on its own) |
| `rotate` | `0`, `90`, `180`, `270` | `0` | rotates the screen (takes a reboot) |
| `cec` | `on`, `off` | `on` | switch the TV's input when music starts |

On every boot, `syncphony-box-config.service` runs `boxd config apply`:
it parses the file into `/etc/syncphony-box/config.env` before the kiosk
starts; `resolution` and `rotate` also become a `video=HDMI-A-1:…D` kernel
argument (the `D` forces the output on, so a TV that's off at boot still
gets a picture later).

Without a valid `server_url` the box shows its setup screen: a QR code you
scan with a phone on the same network to set the server — no SD card
editing needed. (Editing the card still works too.)

### On-Pi checklist

CI can't boot a Pi; verify these on hardware when one is at hand:

- Boots to the server's `/tv` fullscreen in under ~30 s on a Pi 5.
- `sudo systemctl kill --kill-who=all syncphony-kiosk` (or killing
  Chromium) brings the session back within a few seconds.
- No visible cursor — including with a mouse plugged in and wiggled.
- No crash bubble, and no "restore pages" prompt after pulling power.
- The `/tv` visualizer runs smoothly with GPU rasterization (check
  `chrome://gpu` on the box); if not, tune the GPU flags in the wrapper.
- Edit `syncphony.txt` on a laptop (Notepad is fine), boot, and the server
  URL, hostname, audio output and resolution all changed.
- The box answers at `<hostname>.local`; no valid `server_url` still boots
  to the setup screen (QR code); an empty or garbage file boots to defaults.
- A TV that's off at boot gets a picture when turned on later (`D` on the
  video= token), and `rotate=90` really rotates.
- `aplay /usr/share/sounds/alsa/Front_Center.wav` (or `pw-play` on the same
  file) plays the test tone from the chosen output, on both a Pi 4 and a
  Pi 5.
- `pactl info` shows the expected `Default Sink` for each `audio=` value —
  set it on the card, boot, check, and confirm the sink is at 100%.
- Cold boot with the TV **off**: turn the TV on, pair with audio — sound
  plays through the TV, no login, no SSH (the hotplug monitor switched to
  the HDMI sink when it appeared; if the stream doesn't follow without a
  reload, that's a finding for ADR 0004).
- `audio=usb` with a USB DAC: the DAC is the default sink and the test tone
  plays through it after a reboot.
- Unplug HDMI while music plays, plug it back: audio returns by itself.
- Fresh flash, no `server_url`: the setup screen's QR appears; setting the
  URL from a phone (the box validates it via `/healthz`) brings up `/tv`'s
  pairing code with no SD-card editing.
- Pull the server's network cable: the offline screen appears within a
  couple of minutes (or at the next kiosk start) with the box's IP and a
  countdown; plugging it back returns to the room with no touch.
- An hour of real playback as the room's speaker — Navidrome (MP3 and
  FLAC) and Spotify (Ogg): no gaps between songs, no drift, no clipped
  first notes.


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
boxd/                 the box's Go helper daemon (bridge, config, screens)
config                pi-gen build configuration
build.sh              wraps pi-gen's build-docker.sh (builds boxd first)
Makefile              make image / make test / make lint / make clean
scripts/smoke-test.sh no-hardware image checks (used by CI)
tests/                bats tests pinning the syncphony.txt behavior (used by CI)
docs/adr/             architecture decision records
```

## License

AGPL-3.0, matching Syncphony. See [LICENSE](LICENSE).
