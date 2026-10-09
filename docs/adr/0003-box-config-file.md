# ADR 0003: Box configuration — syncphony.txt on the boot partition

- Status: Accepted
- Date: 2026-10-09
- Deciders: Phase 11 plan (MAD-797), box config issue (MAD-800)

## Context

The box needs per-room settings (which server, what it's called, where the
sound goes, how the screen is set up) that an ordinary person can change by
taking the SD card out and editing a file on whatever computer is at hand —
Windows, macOS or Linux. The file lives on the card's FAT boot partition
(`/boot/firmware`, labeled `bootfs`), the only part every OS can write
without tools. A missing, empty or garbage file must never stop the box
from booting.

## Decisions

### The file

`/boot/firmware/syncphony.txt`, shipped with every option commented out
and a one-line explanation each: `server_url`, `name`, `audio`,
`resolution`, `rotate`, `cec`. Simple `key=value` lines, `#` comments.
The parser accepts a UTF-8 BOM and CRLF line endings, because people will
edit this file in Notepad. Unknown keys and unrecognized lines are ignored
with a log line.

### One parser, two callers

`/usr/lib/syncphony-box/syncphony-box-config` is the single place that
knows the format:

- `syncphony-box-config parse [FILE]` prints the validated result as a
  shell-sourceable env file (single-quoted values, so names like
  `O'Neil's TV` survive). Invalid values warn on stderr and fall back to
  defaults; it always exits 0.
- `syncphony-box-config apply` (the boot path) writes
  `/etc/syncphony-box/config.env` from the file, applies the hostname, and
  syncs the kernel command line.

boxd (MAD-803) takes the parsing over in stage 2 and must keep this exact
behavior; `tests/syncphony-box-config.bats` (25 tests, run in CI without
hardware) pins it. The test hooks — `SB_TXT`, `SB_ENV`, `SB_CMDLINE`,
`SB_HOSTNAME_CMD`, `SB_REBOOT_CMD` env overrides — are what let the apply
path be tested on a dev machine instead of a Pi.

### Defaults, and what each key does

| key | valid values | default | effect |
|---|---|---|---|
| `server_url` | `http(s)://…` | unset | the server whose `/tv` the kiosk opens |
| `name` | any text | unset | display name when pairing; slugified hostname |
| `audio` | `hdmi` `hdmi2` `analog` `usb` | `hdmi` | audio output (applied in MAD-801) |
| `resolution` | `WxH[@refresh]`, e.g. `1920x1080@60` | auto | forces the HDMI mode |
| `rotate` | `0` `90` `180` `270` | `0` | rotates the screen |
| `cec` | `on` `off` | `on` | HDMI-CEC wake/switch (applied in MAD-807) |

An unset or invalid `server_url` makes the kiosk open the local
"no server set" page, which now says to edit `syncphony.txt` on the card.
boxd's setup screen (MAD-803) replaces that page in stage 2.

### hostname and mDNS

`name=Living room TV` becomes the hostname `living-room-tv` (lowercase,
runs of anything but `[a-z0-9]` collapse to one hyphen, trimmed, capped at
63 characters; a name with nothing usable leaves the hostname alone with a
warning). The hostname is only touched when `name` is set, so a hostname
configured in Raspberry Pi Imager (applied by cloud-init on first boot)
survives when the file doesn't name the box — and the every-boot service
wins on the next boot when it does. `avahi-daemon` (new in the image)
advertises `<hostname>.local`; the config unit orders itself before avahi
(and re-announces if it lost the race).

### display settings on the kernel command line

`resolution` and `rotate` become one `video=HDMI-A-1:<mode>D` token
(`1920x1080@60D`, plus `,rotate=90` when rotated; `video=HDMI-A-1:D,rotate=90`
when only rotating). The trailing `D` forces the output on, so a TV that is
off at boot still gets a picture when it turns on; both forms are standard
`video=` syntax (kernel `Documentation/fb/modedb.rst`). The token can't
change at runtime, so `apply` rewrites `cmdline.txt` — replacing any
previous `video=HDMI-A-1:…` token, preserving other connectors' tokens —
and reboots **once**, from inside the oneshot unit, before the kiosk starts.
After that boot the desired token is already there, so the second run finds
nothing to do; a failed write logs and continues instead of rebooting.

### The unit

`syncphony-box-config.service`: oneshot, `WantedBy=multi-user.target`,
`RequiresMountsFor=/boot/firmware` (never parse or write the shadowed
directory), ordered `Before=avahi-daemon.service
systemd-avahi-daemon.socket syncphony-kiosk.service`. The script exits 0 on
every bad-config path, so the unit — and the boot — stays green; the kiosk
reads `config.env` and falls back to the "no server set" page if it is
missing or has no valid `SERVER_URL`.

## Consequences

- The kiosk wrapper no longer reads the stage-1 stopgap
  `/etc/syncphony-box/url`; the URL comes from `config.env`.
- CI gains a `tests` job (`bats tests/`, no hardware or image build), and
  the smoke test asserts the file ships on the boot partition fully
  commented out, the unit is enabled, avahi is installed, and the wrapper
  reads `config.env`.
- On-Pi verification (README checklist): that `rotate=` is honored by the
  vc4 driver over HDMI, that the force-on `D` brings a picture to a TV
  that was off at boot, and that editing the file on a laptop changes the
  URL, hostname, audio output and resolution on the next boot.
