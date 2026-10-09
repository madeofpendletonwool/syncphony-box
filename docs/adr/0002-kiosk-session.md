# ADR 0002: Kiosk session — cage + Chromium on tty1

- Status: Accepted
- Date: 2026-10-09
- Deciders: Phase 11 plan (MAD-797), kiosk session issue (MAD-799)

## Context

The box must boot straight to a fullscreen Chromium showing the room on the
TV: no desktop, no login, no cursor, no boot noise, and no way for a crash
or a pulled power cord to leave it on a prompt or a "restore pages?" bubble.

## Decisions

### Seat access: logind, not a seatd daemon

cage (Debian `cage` 0.2.0 / wlroots 0.18) gets its seat through libseat,
whose Debian package depends on `seatd | logind`. The image runs systemd,
so the alternative resolves to **logind**: `syncphony-kiosk.service` opens
the session with `PAMName=login` and `TTYPath=/dev/tty1`, which gives cage
a real logind session (VT control, DRM master, input devices). No seatd
daemon is installed. The `kiosk` user's `video`/`render`/`audio`/`input`
group memberships are belt-and-braces on top of the session.

### The session unit

`syncphony-kiosk.service` (system unit, `User=kiosk`, enabled at build
time):

- runs on tty1: `Conflicts=getty@tty1.service`, `TTYReset=yes`,
  `TTYVDisown=yes`, `UtmpIdentifier=tty1`
- `After=`/`Wants=network-online.target` so Chromium doesn't start into an
  unconfigured interface
- `Restart=always`, `RestartSec=2`: a killed Chromium (cage exits when its
  only client goes) brings the session back within seconds

### All Chromium flags in one wrapper

`/usr/lib/syncphony-box/kiosk` is the only place Chromium is invoked; later
issues (boxd, the `/data` profile move, GPU tuning) edit that file alone.
On top of the kiosk flag set it carries the GPU quartet
(`--ignore-gpu-blocklist --enable-gpu-rasterization
--enable-native-gpu-memory-buffers --enable-zero-copy`): Chromium
blocklists the Pi's vc4/v3d GPU, which would force software rasterization —
too slow for the `/tv` visualizer on a Pi 4. **These flags are chosen from
documented Pi kiosk practice; smoothness on real Pi 4 / Pi 5 hardware is
still to be verified** (see the checklist in the README) and tuned in the
wrapper if needed.

### What the browser opens

*(The URL source below was superseded by the boot config file: the wrapper
now reads `SERVER_URL` from `/etc/syncphony-box/config.env`, written from
`/boot/firmware/syncphony.txt` — see ADR 0003. The fallback page and its
behavior are unchanged.)*

Until the boot config file lands (syncphony.txt → `/etc/syncphony-box/config.env`,
MAD-800) the server URL is read from `/etc/syncphony-box/url` (one line,
the server origin; a trailing `/tv` is tolerated and normalized away).
Anything that isn't an `http(s)` URL — or no file at all — opens the local
`/usr/lib/syncphony-box/unconfigured.html` page instead, so an unconfigured
box shows something intentional rather than a Chromium error. boxd's setup
screen (MAD-803) replaces this.

### Boot presentation

Kernel command line gains `quiet splash loglevel=3
vt.global_cursor_default=0 consoleblank=0`; `config.txt` gains
`disable_splash=1`. With no plymouth in Lite, `splash` is inert today but
already correct if plymouth is ever added; `quiet loglevel=3` is what
actually hides kernel messages, `vt.global_cursor_default=0` removes the
blinking console cursor, and `consoleblank=0` stops the console timing out
to black. cage itself never blanks an output, and `/tv` renders with
`cursor: none` — over a Wayland surface that hides the compositor cursor
sprite too, so a plugged-in mouse leaves nothing on screen once it stops
over the page (hardware checklist item).

## Consequences

- The whole session is three files: the unit, the wrapper, and the fallback
  page, plus one user — all installed by `stage-syncphony/01-kiosk`, with
  boot tuning in `stage-syncphony/02-boot-config`.
- The CI smoke test asserts the user, groups, unit enablement, wrapper, and
  boot-parameter edits inside the built image; on-Pi behavior (30 s boot,
  crash recovery, cursor, restore-prompt) is verified on hardware.
