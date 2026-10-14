# Testing the box on hardware

CI can't boot a Pi. The unit tests (bats + Go, `make test`) and the image
smoke test (`scripts/smoke-test.sh`, runs in CI on every build) pin the
image's structure; the checks below are for real hardware. The README's
on-Pi checklist covers the everyday list — this page is the destructive
one.

## The watchdog gauntlet (MAD-806)

**Done-when for the watchdogs:** a frozen Chromium renderer brings the
room back within about 2 minutes, and a forced kernel hang reboots the
box, which comes back playing. Run both on a box that is paired and
playing music.

### Freeze the renderer (the boxd kiosk watchdog)

```sh
pkill -STOP -f 'type=renderer'      # freeze Chromium's renderer process
journalctl -u boxd -f               # watch the watchdog work
```

Expected timeline: the music keeps playing for a moment (or stops), the
page freezes on screen, and after ~90 s without a heartbeat — while the
server still answers `/healthz` — boxd logs
`page heartbeats stopped while the server answers` and restarts the
kiosk. The room is back, and the speaker resumes by itself, within about
2 minutes of the freeze. Verify the page is interactive again (queue a
song from a phone).

Repeat the freeze twice more inside 15 minutes: the third stall must
log `...kiosk restarts within 15m0s...; rebooting` and reboot the box
rather than restart a fourth time. After it comes back, everything plays.

Also worth one pass: pull the server's network cable instead — the
watchdog must stay quiet (it's the offline screen's job), and no restart
loop appears in the journal.

### Force a kernel hang (the hardware watchdog)

```sh
sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq'   # allow the crash trigger
sudo sh -c 'echo c > /proc/sysrq-trigger'      # panic, immediately
```

Expected: the box dies instantly (this is a real panic — nothing in
userspace can react), the hardware watchdog hard-resets it within ~15 s
of the hang, and it boots back into the room and resumes playing with
no touch. This works because systemd pets `/dev/watchdog` every ~7.5 s
(`RuntimeWatchdogSec=15s`); the panic stops the petting.

Both on a Pi 4 and a Pi 5, please — the watchdog device and `sysrq`
behave the same in theory, but this test exists to check that.

### The 12-hour party soak (Chromium memory)

The question: does Chromium's memory grow without bound with the
visualizer on, on a 2 GB Pi 4? Procedure:

1. Pair the box as the room's speaker and play a long queue with the
   visualizer on, overnight (~12 h).
2. Sample memory hourly (or read it out of the journal afterwards):

   ```sh
   ps -o pid=,rss=,args= -C chromium | sort -k2 -n | tail -3
   ```

3. Note the renderer's RSS over time.

Pass: the total flattens (a sawtooth as pages reload is fine — the
nightly reload at 05:00 only fires while idle, so a party that runs
through the night must not be interrupted; check the journal for when
the reload actually happened). Fail: steady growth past ~1.2 GB total,
OOM kills in the journal (`journalctl -k | grep -i oom`), or audio
dropouts late in the run. If it fails, the finding goes with the
watchdog issue's follow-ups: the nightly reload is already the
mitigation, the next step would be a more aggressive reload policy.

### Thermals and time sync

- `journalctl -u boxd | grep thermals` — one line per minute with
  `temp=` and `throttled=`. During the soak above, the temperature
  should stay under the soft limit (~80 °C on a Pi 4); a persistent
  nonzero `throttled=` (e.g. `0x50000`, which means it *has* throttled
  or browned out before) points at the case or the power supply.
- `curl -s http://127.0.0.1:8099/v1/info` includes `temperatureC` and
  the throttle fields.
- `timedatectl` → "System clock synchronized: yes" while the kiosk is
  up; `systemctl status systemd-timesyncd` → active. Check on both a
  Pi 4 and a Pi 5 (the Pi 5 has an RTC header, but it has no battery by
  default, so it still needs the network).

## The power-pull gauntlet (50 pulls)

**Done-when for the read-only root (MAD-805):** 50 random power pulls —
some during boot, some during playback, some seconds after pairing —
leave a box that boots, is still paired, and plays.

### Setup

- A box flashed with the current image, paired to a Syncphony server,
  playing.
- Any smart plug that can be power-cycled on a schedule (a cheap Wi-Fi
  plug with a repeating timer is enough; this is the "smart plug on a
  timer" the issue calls for). Suggested cycle: **on 2 minutes, off 30
  seconds, repeat**. That lands pulls all over the boot/playback
  timeline without babysitting. For the pairing-adjacent pulls, cut power
  by hand 5–15 s after the pairing code disappears.

### Procedure

1. Confirm the layout first: `findmnt /` shows `overlay`, `findmnt /data`
   shows the ext4, `findmnt /boot/firmware` shows `ro`.
2. Set the plug cycling. Let it run through **40 cycles** unattended.
3. While it cycles, do **5 pulls by hand during boot** (cut 5–20 s after
   applying power) and **5 pulls by hand within 10 s of pairing** (unpair
   with `sudo syncphony-box reset` between them; the box re-shows a code).
4. After **every** power restore, check the three things:
   - the box boots to the room (no setup screen, no error page);
   - it's **still paired** (shows as the room's screen in the app, no new
     pairing code);
   - playback works (start something from the app; sound comes out).
5. Record the result of each pull (a tick sheet is fine — pass/boot,
   pass/paired, pass/plays).

### Pass criteria

All 50: boots, paired, plays. If any pull loses the pairing,
`journalctl -u boxd -b` on the *recovered* boot should show the
`paired event; syncing disks` line from the pull that lost it — that's
the finding to report, along with which pull number and what the box was
doing. A box that fails to boot at all is a different, more serious
finding: capture `journalctl -b -1` after a manual recovery if possible.

### Reading the logs afterwards

Logs persist across power pulls (journald on `/data`, 50 MB cap), so
`journalctl -b -1` reaches the boot *before* the current one:

```sh
journalctl -b -1 -u boxd -u syncphony-kiosk   # the boot that got cut
journalctl --list-boots | wc -l               # one per pull
```

## Related one-off checks

- **Overlay really discards writes:** `sudo touch /usr/bin/nope`, reboot,
  `ls /usr/bin/nope` → gone. `findmnt /` says `overlay`.
- **Boot partition stays ro:** `touch /boot/firmware/nope` →
  `Read-only file system`. The setup form (QR flow) still saves — boxd
  remounts around its write.
- **First-boot grow:** on a fresh flash, `lsblk` shows `/data` grown to
  fill the card after the first boot, and
  `systemctl status syncphony-data-grow` ran once.
- **A destroyed /data still boots:** (sacrificial card) `dd` garbage over
  the third partition from another machine, boot → the TV shows the
  storage-lost screen and the box is reachable; fix by re-flashing.
- **Maintenance path:** `sudo syncphony-box chroot`, `apt update` inside,
  exit, reboot — the update is on the real root
  (`grep -r . /media/root-ro/usr/bin/nope`-style spot check, or a package
  version).
- **Imager user survives:** flash with a username/password in the Imager,
  boot, reboot, SSH in with it — works (the identity restore/save round
  trip). SSH host keys identical across two reboots:
  `ssh-keygen -lf /data/system/ssh/ssh_host_ed25519_key.pub`.
