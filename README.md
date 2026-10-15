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
- A case with a heatsink or a fan. A long party with the visualizer on
  warms a Pi in a closed case past its soft temperature limit, and the
  firmware silently halves the CPU first (the box logs temperature and
  throttling — see [Self-healing](#self-healing-watchdogs) below).

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
`syncphony.txt` behavior and the `/data` persistence script against
binaries built from the repo, plus `go test` for boxd — no hardware or
image build needed; install bats (`apt install bats`) and Go first.
`make lint` checks gofmt and go vet.
CI runs them on every PR and push.

### Continuous integration
GitHub Actions builds the image natively on arm64 runners
(`ubuntu-24.04-arm`) on every push to `main` and on every PR, runs the
bats and Go tests, runs the no-hardware smoke test (`scripts/smoke-test.sh`
loop-mounts the built image and checks packages, enabled units, the locked
first user, the first-boot seed files, the kiosk session, the boot
config file, the audio stack, boxd, and the read-only-root layout: three
partitions, the overlay wired into the initramfs, and every persistent
path on /data), and uploads the image as a workflow artifact.

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
  listener is on the LAN only while the box is unconfigured (or for the
  10 minutes after a long OK press on the TV remote), and
  it never offers anything beyond the URL and the box's name.
- **Offline.** The kiosk never navigates straight at the server: it opens a
  local start page that checks `/healthz` first. If the server can't be
  reached, the TV shows "Can't reach `<server>` — retrying" with the box's
  IP and a countdown, and goes back to the room by itself once the server
  answers — never Chromium's error page. If the server goes away while the
  room is open, boxd notices (heartbeats keep coming while the page is
  alive) and moves the TV to the offline screen after a couple of minutes.

- **No network at all: the setup hotspot.** A box with no Ethernet and
  no Wi-Fi becomes its own open Wi-Fi network, `Syncphony-Box-XXXX`
  (the last four of its MAC). The TV shows a QR code that joins it, and
  the phone's captive-portal page opens with the Wi-Fi networks in range,
  a password field, and the server URL if the box doesn't have one yet.
  Saving joins that Wi-Fi. If the join fails, the hotspot comes back and
  the TV says why. A new box starts the hotspot 60 s after boot. A box
  that has been online before shows the offline screen first and only
  falls back to the hotspot after 5 minutes without a network, so a
  router reboot never puts it in setup mode. Imager's Wi-Fi settings
  still work and are the fast path
  ([ADR 0010](docs/adr/0010-wifi-setup-hotspot.md)).

Logs go to journald: `journalctl -u boxd -u syncphony-kiosk`.

## The TV remote (HDMI-CEC)

The box talks CEC through the kernel framework (`cec-ctl`), not libcec —
see [ADR 0009](docs/adr/0009-hdmi-cec.md). Three behaviors, all under
`cec=` in `syncphony.txt`:

- **Wake and switch.** When music starts, a TV in standby (or on another
  input) gets *Image View On* + *Active Source* — queue a song from the
  couch and the room turns itself on. Rate-limited to once per playback
  start and once every 5 minutes, so it never fights whoever switched to
  the game console.
- **The remote's keys.** Arrows, OK, Back and Play/Pause on the TV remote
  become keyboard events (injected through uinput) — `/tv` already knows
  them: arrows steer the visuals, right skips, OK clicks (the "Play here
  instead" button is focused, so OK takes over as speaker), Play/Pause
  plays and pauses. Holding **OK for 3 seconds** opens the setup window
  and shows the box's IP and version on the TV.
- **Optional standby.** `cec_standby=on` puts a TV the box switched on
  (and nobody has taken away since) into standby after the room has been
  idle for `cec_standby_minutes` (default 30). Off by default.

CEC support varies wildly by TV brand — what works where is recorded in
[docs/hardware.md](docs/hardware.md).

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

## Staying paired

The box's whole identity is its Chromium profile: the server's
`syncphony_display` pairing cookie and the "I'm the speaker" localStorage
flag live there, so that's what must survive reboots and power cuts. The
profile lives on the **/data partition** (`/data/chromium` — see below);
the path is defined in one place (boxd's `DefaultProfileDir`) and reaches
the kiosk wrapper through `config.env`. See
[docs/adr/0006-pairing-persistence.md](docs/adr/0006-pairing-persistence.md).

Two mechanisms keep it durable:

- When the `/tv` page reports a `paired` or `unpaired` event on the box
  bridge (and, rate-limited, a playback start), boxd runs a `sync` burst —
  Chromium writes cookies and localStorage lazily, and the burst pushes
  those writes to the SD card seconds after they happen instead of up to
  half a minute later. Pair, pull the power within 10 s, boot: still
  paired.
- Before every start the wrapper clears Chromium's "exited uncleanly"
  state, so a power cut never turns into a "restore pages?" bubble on
  the TV.

**Resetting a box.** When a box moves to another room or another server,
wipe the pairing so it shows a new code:

```sh
sudo syncphony-box reset
```

In a dev checkout, `make reset` does the same to a profile (point it with
`SB_PROFILE=...`; it defaults to the box's path).

## Safe to unplug: read-only root and /data

The box is meant to be power-cycled like a TV — pulled at any moment,
mid-song included. The image makes that structurally safe
([docs/adr/0007](docs/adr/0007-readonly-root-and-data.md)):

- **Three partitions:** boot (FAT), root (ext4), and **data** (ext4,
  label `syncphony-data`). On first boot the data partition grows to fill
  the card; the root never grows.
- **The root filesystem is read-only behind an overlay** — the mechanism
  raspi-config's "Overlay FS" option uses, enabled at build time: the
  real root is the lower layer, every runtime write lands in a tmpfs
  upper and evaporates at power-off. Nothing on the SD card is ever
  half-written.
- **Everything that must persist lives on /data** — the pairing (the
  Chromium profile), the applied config, logs, the SSH host keys, the
  Imager-created user, Wi-Fi credentials. The maintained list is
  [docs/persistent-paths.md](docs/persistent-paths.md).
- **/boot/firmware stays read-only** too; boxd remounts it rw for the
  moment it writes `syncphony.txt` (the setup form) or a `resolution=`
  change, then ro again.
- **A damaged /data never hangs the boot**: the box comes up unpaired
  and the TV says the storage is lost (fix: check or re-flash the card).

The power-pull test procedure (50 pulls: during boot, during playback,
seconds after pairing) is [docs/testing.md](docs/testing.md).

**Maintaining the box.** Runtime writes under `/` are discarded by
design, so package updates go to the real root through a chroot (the
overlayroot-chroot role in this image):

```sh
sudo syncphony-box chroot            # shell on the real root, read-write
# (or: sudo syncphony-box chroot apt full-upgrade)
```

Reboot afterwards. Changes to the pairing, config and anything else on
`/data` need no chroot — that partition is writable at runtime.

## Self-healing: watchdogs

The box notices when it's stuck and fixes itself — nobody walks over to
it. Three watchdogs, each owning one failure class
([ADR 0008](docs/adr/0008-watchdogs-and-self-healing.md)):

- **Hardware watchdog (kernel/systemd hangs).** systemd arms the Pi's
  hardware watchdog at 15 s. A kernel or systemd hang stops the petting
  and the hardware hard-resets the box; with the read-only root it comes
  back clean, paired, playing.
- **Kiosk watchdog in boxd (a wedged page).** The `/tv` page heartbeats
  to boxd every 30 s. If the heartbeat stops for 90 s while the server
  still answers `/healthz`, boxd restarts the kiosk — Chromium comes
  back and the speaker resumes by itself. If that happens 3 times in 15
  minutes, the box reboots instead. When the server is *unreachable*,
  the watchdog stays quiet: that's the offline screen's job, not a
  stuck page.
- **Time.** `systemd-timesyncd` runs and the kiosk waits for
  `time-sync.target`: the server's playback clock sync trusts the
  browser's clock, and a Pi has no RTC battery by default.

Two hygiene loops ride along:

- **Thermals.** boxd logs `vcgencmd measure_temp` and the throttling
  flags to the journal every minute (`journalctl -u boxd | grep
  thermals`) and reports them in `/v1/info` for the room's Screens list
  (stage 4). A persistent `throttled=0x50000`-style value means the
  power supply is weak; a hot `temp=` means the case needs airflow.
- **Nightly reload.** Once a day around 05:00 local, while the page is
  up and nothing is playing, boxd restarts the kiosk so a long-lived
  Chromium gives its memory back. A party that runs through the night
  is never interrupted.

The freeze and kernel-hang procedures that verify all this are in
[docs/testing.md](docs/testing.md).

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
| `cec` | `on`, `off` | `on` | switch the TV's input when music starts (HDMI-CEC; the remote's keys drive the box too — [docs/hardware.md](docs/hardware.md) for the TV matrix) |
| `cec_standby` | `on`, `off` | `off` | put the TV in standby once the room has been idle (only ever a TV the box switched on itself) |
| `cec_standby_minutes` | `1`–`240` | `30` | how many idle minutes before that standby |

On every boot, `syncphony-box-config.service` runs `boxd config apply`:
it parses the file into `config.env` (kept on `/data`; `/etc/syncphony-box/config.env`
is a symlink to it) before the kiosk starts; `resolution` and `rotate`
also become a `video=HDMI-A-1:…D` kernel argument (the `D` forces the
output on, so a TV that's off at boot still gets a picture later).

Without a valid `server_url` the box shows its setup screen: a QR code you
scan with a phone on the same network to set the server — no SD card
editing needed. (Editing the card still works too.)

### On-Pi checklist

CI can't boot a Pi; verify these on hardware when one is at hand. The
destructive power-pull gauntlet (the done-when for the read-only root)
has its own page: [docs/testing.md](docs/testing.md).

- Boots to the server's `/tv` fullscreen in under ~30 s on a Pi 5.
- `sudo systemctl kill --kill-who=all syncphony-kiosk` (or killing
  Chromium) brings the session back within a few seconds.
- No visible cursor — including with a mouse plugged in and wiggled.
- No crash bubble, and no "restore pages" prompt after pulling power.
- Pair → reboot → the box is back in the room and playing without a touch.
- `sudo syncphony-box reset` wipes the pairing: the box shows a fresh
  pairing code afterwards, and re-pairs normally.
- Read-only root: `findmnt /` shows an overlay, `/boot/firmware` is `ro`,
  `/data` is mounted, `touch /usr/bin/nope` survives only until a reboot,
  and the setup form still saves (boxd remounts the boot partition around
  its write).
- First boot grows `/data` to fill the card (`lsblk`), root stays at
  image size.
- `sudo syncphony-box chroot apt update` writes the real root; the change
  is still there after a reboot.
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
- HDMI-CEC ([docs/testing.md](docs/testing.md), "The CEC gauntlet"): a
  TV in standby wakes and switches when a song is queued; the remote's
  keys drive `/tv`; a 3-second OK shows the box's IP and version; the
  box never fights a hand-switch to another input. Record the TV in
  [docs/hardware.md](docs/hardware.md).
- `pactl info` shows the expected `Default Sink` for each `audio=` value —
  set it on the card, boot, check, and confirm the sink is at 100%.
- Cold boot with the TV **off**: turn the TV on, pair with audio — sound
  plays through the TV, no login, no SSH (the hotplug monitor switched to
  the HDMI sink when it appeared; if the stream doesn't follow without a
  reload, that's a finding for ADR 0004).
- `audio=usb` with a USB DAC: the DAC is the default sink and the test tone
  plays through it after a reboot.
- Unplug HDMI while music plays, plug it back: audio returns by itself.
- Fresh flash, no Imager Wi-Fi, no cable: about a minute after boot the
  TV shows the `Syncphony-Box-XXXX` QR; a phone joins, the setup page
  opens by itself, and choosing the house Wi-Fi (plus the server URL)
  ends in the pairing screen. A wrong password brings the hotspot back
  with the reason on the TV ([docs/testing.md](docs/testing.md), "The
  Wi-Fi setup run").
- Fresh flash, no `server_url`: the setup screen's QR appears; setting the
  URL from a phone (the box validates it via `/healthz`) brings up `/tv`'s
  pairing code with no SD-card editing.
- Pull the server's network cable: the offline screen appears within a
  couple of minutes (or at the next kiosk start) with the box's IP and a
  countdown; plugging it back returns to the room with no touch.
- Freeze Chromium's renderer (`pkill -STOP -f 'type=renderer'`): the
  room comes back by itself within about 2 minutes (the boxd kiosk
  watchdog), and music resumes.
- Kernel-hang reboot (destructive; see docs/testing.md):
  `echo c > /proc/sysrq-trigger` — the hardware watchdog resets the box
  and it comes back paired and playing.
- `journalctl -u boxd | grep thermals` shows a temperature line every
  minute; `curl 127.0.0.1:8099/v1/info` includes `temperatureC`. Check
  on a Pi 4 and a Pi 5.
- `timedatectl` shows the clock synchronized and
  `systemctl status systemd-timesyncd` is active, on both a Pi 4 and a
  Pi 5 (which has an RTC header but no battery by default).
- A 12-hour playback soak with the visualizer on, on a 2 GB Pi 4 (the
  procedure is in docs/testing.md): memory plateaus, and the 05:00
  nightly reload happens only while idle.
- An hour of real playback as the room's speaker — Navidrome (MP3 and
  FLAC) and Spotify (Ogg): no gaps between songs, no drift, no clipped
  first notes.


## Flashing

1. Write the `.img.xz` to an SD card with Raspberry Pi Imager.
2. In the OS customization settings, set at minimum a username and password
   (Wi-Fi, SSH and hostname as needed — without Wi-Fi settings or a cable,
   the box offers its setup hotspot instead). The first user in the image ships with
   a **locked password** — there is no default login; the Imager settings are
   applied on first boot by cloud-init.
3. Boot the Pi. Without Imager customization the image behaves like stock
   Raspberry Pi OS Lite and asks for a username and password on the console.

## Repo layout

```
pi-gen/               pi-gen submodule (pinned, never edited)
stage-syncphony/      custom pi-gen stage (packages, kiosk session,
                      read-only root, CEC, Wi-Fi setup, ...)
boxd/                 the box's Go helper daemon (bridge, config, screens,
                      watchdogs, CEC, Wi-Fi setup hotspot)
config                pi-gen build configuration
build.sh              wraps pi-gen's build-docker.sh (builds boxd first,
                      then appends the /data partition after export)
Makefile              make image / make test / make lint / make clean
scripts/add-data-partition.sh  the /data partition post-export step
scripts/smoke-test.sh no-hardware image checks (used by CI)
tests/                bats tests pinning the syncphony.txt and /data
                      behavior (used by CI)
docs/adr/             architecture decision records
docs/persistent-paths.md  the list of everything that survives a power cut
docs/hardware.md      tested boards and the TV CEC compatibility matrix
docs/testing.md       the hardware test procedures (power pulls & co)
```

## License

AGPL-3.0, matching Syncphony. See [LICENSE](LICENSE).
