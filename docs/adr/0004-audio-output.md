# ADR 0004: Audio output — PipeWire as the kiosk user, sink picked from audio=

- Status: Accepted
- Date: 2026-10-09
- Deciders: Phase 11 plan (MAD-797), box audio issue (MAD-801)

## Context

The box is the room's speaker: Chromium plays everything (Navidrome
transcodes, Spotify Ogg proxied by the server) through an `<audio>` element
on `/tv`. Sound must come out of the output named by `audio=` in
`syncphony.txt` (`hdmi`, `hdmi2`, `analog`, `usb`), at boot, with nobody
logged in — and survive HDMI hotplug: a TV that is off at boot and turned on
later must get sound with no login and no SSH.

## Decisions

### PipeWire, not plain ALSA

PipeWire (the Raspberry Pi OS default stack: `pipewire` + `pipewire-pulse`
+ `wireplumber`) over plain ALSA, because:

- **HDMI hotplug is the hard requirement**, and WirePlumber handles it: it
  watches udev and ALSA jack state, creates the sink when the display link
  comes up, and deactivates it when it goes away. Plain ALSA has nothing in
  the middle — the app holds a device that stops working, and the only fix
  is restarting it.
- **Chromium speaks PulseAudio** (libpulse client against
  `pipewire-pulse`); that is its primary, best-tested Linux path. Its ALSA
  backend is a fallback. Output switching is `pactl set-default-sink`; in
  plain ALSA it would mean rewriting `asound.conf` and restarting Chromium.
- Pi OS ships, defaults to, and tests this stack on exactly our boards
  (Pi 4 and Pi 5, Bookworm and Trixie). Plain ALSA would be a bespoke
  configuration on top of a distro that no longer expects it.

What we give up: a few tens of MB of image size, and simplicity. What we
keep: volume and output control from one CLI (`pactl`), which the monitor
below needs anyway.

### Per-user PipeWire for `kiosk` via linger, not system mode

PipeWire's user services (`pipewire.service`, `wireplumber.service`,
`pipewire-pulse.socket`) normally start with a login session. The kiosk
never logs in interactively. Two options:

- **PipeWire system mode** — explicitly unsupported upstream and fragile
  with WirePlumber. Rejected.
- **`loginctl enable-linger kiosk`** — `/var/lib/systemd/linger/kiosk`
  makes systemd start the kiosk user's manager (`user@<uid>.service`) at
  boot, with no session. The image links the PipeWire/WirePlumber user
  units into the kiosk user's `default.target.wants` /
  `sockets.target.wants` (under `/var/lib/syncphony-box/.config/systemd/
  user/`), so the whole audio stack comes up on its own at boot. Chosen.

Nothing about this needs the kiosk session unit; both units run as
`User=kiosk` and derive `XDG_RUNTIME_DIR=/run/user/$(id -u)` (the pulse
socket lives there) since system services get no PAM environment.

### Where the sound goes: match sinks, don't name them

`/usr/lib/syncphony-box/audio-setup` reads `AUDIO=` from `config.env` and
matches sinks from `pactl list sinks` against markers, in order:

| `audio=` | matches (first hit wins) | on the hardware |
|---|---|---|
| `hdmi` | `bcm2835 hdmi 1`, `vc4-hdmi-0`, `hdmi0` | HDMI0, the port next to USB-C power |
| `hdmi2` | `bcm2835 hdmi 2`, `vc4-hdmi-1`, `hdmi1` | HDMI1 |
| `analog` | `headphones` (non-USB), or `bcm2835` without `hdmi` | the Pi 4's 3.5 mm jack; the Pi 5 has none |
| `usb` | `device.bus = "usb"`, else `usb` in the sink text | the first USB audio device (DAC, speakers) |

The primary HDMI markers are the ALSA PCM names the vc4 driver gives its
two HDMI outputs (`bcm2835 HDMI 1` / `bcm2835 HDMI 2`) — the same driver
on Pi 4 and Pi 5 — so the mapping does not depend on card order or on
PipeWire's sink-name mangling (sink names flip between `hdmi-stereo` and
`stereo-fallback` depending on the TV's ELD; the markers don't).

A configured output that is absent (TV off, no DAC, `analog` on a Pi 5) is
a **warning, never a boot failure**: the default sink is left alone
(WirePlumber's own default, usually HDMI) until the output appears and the
monitor (below) switches to it. No silent wrong-port fallback: markers are
explicit, so an unmatched naming change is loud.

### Volume: 100%, unmuted, and never re-scaled

At boot — and after any hotplug that re-applies the selection — the chosen
sink is set to 100% and unmuted (`pactl set-sink-volume … 100%`,
`set-sink-mute … 0`). Nothing in the stack ducks or attenuates: the level
the room hears is set by the TV's remote (HDMI carries PCM; the TV owns
the volume) or the room's own volume control, per the plan. Sinks without
a hardware volume control (the Pi's jack) get software volume pinned at
unity, which is the same "no scaling" guarantee in practice.

WirePlumber's device auto-suspend is turned off for our outputs
(`.config/wireplumber/wireplumber.conf.d/50-syncphony-box.conf`,
`session.suspend-timeout-seconds = 0`): waking an ALSA device on the first
sound can clip its opening notes — audible as ticks between tracks, which
the checklist listens for.

### Chromium never starts before the audio server; audio follows hotplug

Mirroring how the kiosk waits for `network-online.target`:

- `syncphony-audio.service` (oneshot, before the kiosk, `Wants=`d by it):
  waits for the PipeWire server (`pactl info`, ≤ 45 s), waits briefly for
  the configured sink (≤ 8 s — a TV that's off must not hold up boot),
  then applies the selection. It always exits 0, like the config service.
- `syncphony-audio-monitor.service` (long-running, `Restart=always`):
  re-applies the selection on `pactl subscribe` sink/server/card events —
  the TV-off-at-boot-then-on case and the HDMI-unplugged-then-back case.
  Applies are state-checked (only issue `set-*` calls when something
  actually differs) so the monitor's own changes don't re-trigger it.

Whether Chromium's `<audio>` stream follows a default-sink change without
a reload is the one behavior we could not verify without hardware; it is
first on the audio checklist items in the README. If it doesn't, the
fallback is the stage 3 watchdog restarting the kiosk on sink topology
changes.

## Consequences

- `stage-syncphony/04-audio` installs the stack, the two units, the
  kiosk-user PipeWire enablement (linger + wants symlinks) and the
  WirePlumber conf; the kiosk unit gains `After=`/`Wants=
  syncphony-audio.service`.
- `tests/syncphony-box-audio.bats` pins the sink matching and the
  volume/default policy against a fake `pactl` with representative
  listings for a Pi 4 (two HDMI + jack), a Pi 5 (two HDMI) and a USB DAC;
  the smoke test checks packages, units, linger, symlinks and the conf
  file inside the built image.
- On-Pi verification (README checklist): the exact ALSA strings behind the
  markers on real Pi 4/Pi 5 Trixie builds, stream-follows-hotplug, the
  hour-long gap/drift listen, and the cold-boot-TV-off done criteria.
