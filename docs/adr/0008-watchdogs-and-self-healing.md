# ADR 0008: Watchdogs and self-healing

- Status: Accepted
- Date: 2026-10-14
- Deciders: Phase 11 plan (MAD-797), watchdog issue (MAD-806)

## Context

The box hangs on a living-room wall: nobody is standing next to it when
Chromium's renderer wedges, the kernel panics, or the clock drifts into
years past. A box that needs someone to walk over and pull the plug has
failed at being an appliance. Stage 2 added the heartbeat (Syncphony
ADR 0016: the page beats every 30 s while healthy) and the offline
screen's restart; this ADR is the rest of the noticing-and-fixing.

## Decision

Three watchdogs, layered so each failure has exactly one owner.

### 1. Hardware watchdog: systemd arms the BCM watchdog at 15 s

`RuntimeWatchdogSec=15s` (drop-in in `stage-syncphony/07-watchdog`) —
PID 1 pets `/dev/watchdog` at half the interval; a kernel or systemd
hang stops the petting and the hardware hard-resets the Pi. This is the
only watchdog that catches the kernel itself. `RebootWatchdogSec=2min`
bounds a reboot that hangs partway. The read-only root (ADR 0007) is
what makes a hard reset safe: the box comes back clean, paired, playing.

### 2. Kiosk watchdog in boxd: the heartbeat policy

`boxd` watches the bridge heartbeat every 5 s
(`boxd/internal/daemon/watchdog.go`). The policy, as one pure decision:

- **Stall → restart.** The page's heartbeat has been silent for 90 s
  (three missed 30 s beats) while the server's `/healthz` answers →
  restart `syncphony-kiosk.service`. Chromium comes back, the page
  reloads, the speaker resumes on its own (MAD-804). This is the
  `kill -STOP`-on-the-renderer case: the room returns within ~2 minutes.
- **Settle window.** A kiosk (re)start is followed by 4 minutes of
  quiet: cage, Chromium and the page need a minute or two before their
  first heartbeat, and the fresh page must not be restarted out from
  under itself. The same grace covers a boot where no heartbeat ever
  arrived.
- **3 in 15 minutes → reboot.** Watchdog-caused restarts inside a
  15-minute window, and the page stalls again → reboot instead of
  restarting a fourth time. A kiosk boxd cannot keep alive is declared
  boot-sick; the hardware watchdog and the read-only root make the
  reboot cheap.
- **Server unreachable is not the watchdog's problem.** A missing
  heartbeat then means "there is no /tv page" — the kiosk sits on the
  local start screen, which is the offline screen's job (MAD-803): that
  trigger keeps its own 10-minute guard, so no restart loops. The two
  triggers are mutually exclusive on `/healthz`.
- **The storage-lost screen never feeds the watchdog.** Without `/data`
  the kiosk cannot produce a page that beats; rebooting cannot fix the
  card, so that screen is terminal by design (ADR 0007).
- Intentional restarts (`/v1/reload`, the setup save, the nightly
  reload) settle but don't count toward the reboot: care, not symptoms.

### Nightly reload at 05:00, only when idle

Chromium with a long-lived page and the visualizer on slowly grows its
heap; a fresh page gives the memory back. Once a day, during the 05:00
local hour, when the page is up (fresh beats), not playing (bridge
events), and the session has run at least 6 hours, boxd restarts the
kiosk — a reload by the only path that always works. A party running
through the hour skips that night; there is always the next. The
12-hour-visualizer memory check that motivated it is a hardware
procedure in docs/testing.md.

### 3. Thermals: sample, log, report

boxd runs `vcgencmd measure_temp` and `vcgencmd get_throttled`
(`libraspberrypi-bin`, installed with this stage) once a minute, logs
one journal line per sample (`journalctl -u boxd` doubles as a
temperature log, and journald is persistent on /data), and reports the
latest values in `GET /v1/info` — `temperatureC`, `throttled`,
`underVoltage`, `throttleOccurred` — for stage 4's Screens list. The
flags are the firmware's bitmap: current conditions in bits 0-3
(under-voltage, frequency-capped, throttled, soft temperature limit),
the same four as sticky "has occurred" flags in bits 16-19; `throttled`
means limited *right now*. No vcgencmd (a dev machine): thermals are
absent, everything downstream tolerates that. The README recommends a
case with a heatsink or fan; under a long party in a closed case the
soft temperature limit is what silently halves the CPU first.

### Time: timesyncd before the kiosk

`systemd-timesyncd` is installed and enabled, and
`syncphony-kiosk.service` orders `After=`/`Wants=` `time-sync.target`:
the server's playback clock sync trusts the browser's clock, and a Pi
has no RTC battery by default (the Pi 5's RTC header ships without a
battery too). The ordering is best-effort by design — a box with no
network must still reach its setup screen.

## Consequences

- `echo c > /proc/sysrq-trigger` reboots the box within ~15 s and it
  comes back playing; `kill -STOP` on Chromium's renderer brings the
  room back within ~2 minutes. Both procedures are in docs/testing.md.
- boxd's `/v1/info` grows thermal fields beyond ADR 0016's core three;
  unknown fields are ignorable by contract, and stage 4 (MAD-811) is
  their consumer. The bit layout of `get_throttled` is pinned by tests.
- The watchdog restart history lives in memory: a reboot clears the
  count, which is exactly right — the reboot *is* the escalation.
- journald gains one line per minute (~few hundred kB/day against the
  50 MB cap); worth it for a temperature history that survives power
  cuts.
