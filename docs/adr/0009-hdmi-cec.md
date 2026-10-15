# ADR 0009: HDMI-CEC — wake the TV, remote keys

- Status: Accepted (hardware verification pending — see docs/hardware.md)
- Date: 2026-10-15
- Deciders: Phase 11 plan (MAD-797), CEC issue (MAD-807)

## Context

The box hangs from the TV's HDMI port, and HDMI carries a wire the box
can use: CEC. Two things come with it. One-way: when music starts, the
box can bring the TV out of standby and switch it to its own input, so
queueing a song from the couch turns the room on. Two-way: the TV
remote's keys can drive `/tv`, so nobody needs a keyboard to skip a song
or take over the speaker from the couch.

CEC is famously uneven — Samsung's Anynet+, LG's SimpLink and Sony's
Bravia Sync all implement different subsets. The decisions here aim for
the safe common core and degrade quietly where a TV doesn't play along.

## Decision

### The kernel CEC framework through cec-ctl, not libcec

The Pi's `vc4` driver already exposes the CEC adapter as `/dev/cec0`
(`/dev/cec1` for the second HDMI port), and `cec-ctl` (v4l-utils, in
stage-syncphony/08-cec) drives the whole kernel API from a command line.
libcec would add a C++ library dependency for the same bus. boxd claims
the playback logical address (`cec-ctl -d /dev/cec0 --playback`) on both
adapters when it starts — audio=hdmi2 means the TV is on cec1 — and
runs `cec-ctl --monitor` per adapter, parsing the stable textual format
(`USER_CONTROL_PRESSED (0x44):` + `\tui-cmd: select (0x00)`) into events.
No CEC at all (a dev machine) logs once and runs inert.

### Wake and switch, twice rate-limited

On a `playing` bridge event (ADR 0016) — once per playback start, not
per repeat — boxd sends `<Image View On>` and `<Active Source>` (with a
freshly re-read physical address, because hotplugs change it) to the
adapter the TV is on. A second lock inside the controller bounds it to
once every 5 minutes no matter how many playback starts happen, so the
box never fights whoever switched the TV to the game console.
`cec=off` in syncphony.txt disables all of it.

### Remote keys through uinput

CEC `<User Control Pressed>` becomes Linux key events through a uinput
keyboard boxd creates (`/dev/uinput`; the udev rule in
stage-syncphony/08-cec grants the `cec` group — access comes from the
group, not from boxd's root). cage hands the device to libinput and
Chromium sees an ordinary keyboard. The map is the keys `/tv` knows:
arrows → arrow keys, OK → Enter (the "Play here" button is
auto-focused, so Enter clicks it), Back → Escape, Play/Pause →
MediaPlayPause. A release that never arrives (some TVs lose them) is
synthesized after a second, so nothing sticks down.

**OK is hold-to-gesture.** The Enter keydown is held back until the
release proves a short press; holding OK for 3 seconds instead opens the
setup window (the phone form at `http://<hostname>.local`, the same
window as SIGUSR1) and puts the box's name, version and IPs on the TV by
restarting the kiosk onto an info screen that walks back to the room by
itself (the speaker resumes, MAD-804). Without the hold-back, the 3
second press would click whatever is focused before the gesture fired.

### Optional standby, only for a TV the box switched on

`cec_standby=on` (off by default) plus `cec_standby_minutes` (default
30): after the room has been idle (`idle` bridge events) that long,
send `<Standby>` — but only while the TV is on the box *because the box
switched it on*. The monitor watches for a foreign `<Active Source>`
announcing a different physical address and drops the claim the moment
someone else takes the TV; a TV the box never seized (the user picked
the input themselves) is never stood down, and neither is one the
game console now owns.

## Consequences

- `/v1/info` gains the `cec` capability once the adapters registered;
  stage 4's Screens list can show it.
- `cec`, `cec_standby`, `cec_standby_minutes` join syncphony.txt; like
  `resolution=`, they apply at boot.
- The one web-app gap this surfaced is fixed on the Syncphony side:
  `/tv`'s `tvKeyOf` now treats `MediaPlayPause` as play/pause (it
  previously knew only space and K), so the remote's play/pause key
  works out of the box.
- What CI can't cover: real TVs. The brand matrix (Samsung Anynet+, LG
  SimpLink, Sony Bravia Sync) lives in docs/hardware.md, and the
  on-hardware gauntlet in docs/testing.md — both marked pending until
  the bench runs.
