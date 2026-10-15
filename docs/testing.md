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

## The CEC gauntlet (MAD-807)

**Done-when:** with the TV in standby, queueing a song from a phone
turns the TV on, switches it to the box and plays. The TV remote's OK
takes over as speaker when someone else is playing, and play/pause
works. Run on at least two TV brands (Samsung "Anynet+", LG "SimpLink",
Sony "Bravia Sync" — enable CEC in the TV's settings first) and record
the results in [docs/hardware.md](hardware.md).

Watch the daemon's view of the bus while you do:

```sh
journalctl -u boxd -f
```

1. **Wake and switch.** TV in standby (or on the console's input), music
   off. Queue a song from a phone → within a few seconds the TV comes on
   showing the box and the music plays through it. `boxd` logs
   `woke the TV and switched it to this box`.
2. **No fight.** Switch the TV to the game console while music plays.
   Pause, then play again from the phone (a new playback start) → the
   box must *not* yank the TV back within the 5-minute rate limit.
   `cec-ctl -d /dev/cec0 --monitor` should show the console's
   `ACTIVE_SOURCE`, and boxd logs `became the active source; leaving the
   TV alone`.
3. **Remote keys.** Arrows step the visuals, right skips (or votes),
   play/pause toggles, Back leaves to the stage view, and OK clicks
   whatever is focused — with someone else's phone the speaker, the
   "Play here instead" button is focused, so one OK takes the room.
4. **OK long-press.** Hold OK for 3 seconds → the setup window opens
   (the phone form answers at `http://<hostname>.local` for ten
   minutes) and the TV shows the box's name, version and IPs, then
   walks back to the room by itself and the speaker resumes. A short OK
   press must still click — not open the info screen.
5. **Optional standby.** `cec_standby=on` (and a short
   `cec_standby_minutes` for the test) in `syncphony.txt`, reboot. Play
   music (the box seizes the TV), stop it, wait out the minutes → the TV
   goes to standby. Then switch the TV to the console by hand, play and
   stop again → the box must leave the TV on.
6. **`cec=off`.** Set it, reboot, put the TV in standby, queue a song →
   the music plays (through the TV if it was already on the box) but
   nothing is switched or woken; the remote's keys do nothing.

Remote keys are per-TV uneven: if arrows arrive but OK doesn't (or OK
arrives as something odd), capture `cec-ctl -d /dev/cec0 --monitor`
output while pressing keys and add the TV to the table in
[docs/hardware.md](hardware.md).

## The Wi-Fi setup run (MAD-808)

**Done-when:** a box flashed with no Wi-Fi settings and no cable can be
set up end to end from a phone alone. Run it once with an iPhone and once
with an Android phone, because their captive-portal behavior differs.

Flash with Raspberry Pi Imager, setting a user but **no Wi-Fi**, and no
Ethernet cable. A serial console or a keyboard helps you watch
`journalctl -u boxd -f`, but you don't need one.

1. **The hotspot appears.** Boot. The TV shows the setup screen, then
   about a minute after boot it switches to "Connect this box to Wi-Fi"
   with a QR code and `Syncphony-Box-XXXX`. boxd logs
   `setup hotspot Syncphony-Box-XXXX up at 10.42.0.1`.
2. **The portal opens.** Scan the QR with the phone camera and join (no
   password). The phone's captive-portal sheet opens the setup page by
   itself. If it doesn't, open `http://10.42.0.1`: the portal works
   either way, but note which phone needed it.
3. **A wrong password.** Pick the house network and type a wrong
   password. The phone shows "Connecting…" and drops off. The TV shows
   "Joining…", then the hotspot again with "Couldn't join … — is the
   password right?". Rejoin from the phone: the form shows the same
   error.
4. **The real join.** Pick the network, enter the right password, and
   the server URL. The TV shows "Joining…" and then the server's pairing
   code. Pair from the app. `nmcli connection show` lists the network,
   and `ls /data/system/network/` already has it, without waiting
   15 minutes.
5. **It sticks.** Pull the power and boot. The box comes back on the
   Wi-Fi, paired, with no hotspot.
6. **Router reboot.** Power the router off. The TV shows the offline
   screen. Power it back on within 4 minutes: the box comes back to the
   room and the hotspot never appeared.
7. **Moved house.** Leave the router off for more than 5 minutes. The
   hotspot comes up (the box has been online before, so it waited 5
   minutes, not 1). Then, with nobody on the portal, power the router
   back on. Within about 5 minutes the box releases its hotspot, finds
   the saved network, and returns to the room.
8. **Ethernet wins.** With the hotspot up, plug in a cable. The hotspot
   goes down within seconds and the TV goes back to the room (or the
   setup screen, if no server is set).

## The overnight update (MAD-810)

The done-when for updates: a box flashed with release N, left on
overnight after N+1 is published, runs N+1 in the morning and is still
paired. Takes two consecutive releases (cut a throwaway `vX.Y.Z-test`
pair if needed) and one night, or a shortcut with the timer.

1. Flash release N with Imager (from the listing), set Wi-Fi and the
   timezone, and pair the box with a room. Play something, then stop.
2. Publish release N+1 (docs/releasing.md).
3. Leave the box on, idle, past 04:00 box-local time. Shortcut:
   `sudo systemctl start syncphony-update.service` (the nightly path,
   playback check included) instead of waiting.
4. In the morning:
   - `syncphony-box version` prints N+1;
   - `journalctl -u syncphony-update -b -1` shows the upgrade and
     "rebooting into the update"; `last reboot` shows one reboot near 04:00;
   - the TV is in the room, paired, and plays when you queue a song;
   - `findmnt /` is still the overlay, `/boot/firmware` is `ro` again;
   - `curl -s 127.0.0.1:8099/v1/info` has `"update": {"result": "updated", ...}`
     and `"version"` N+1;
   - `getent passwd` still has the Imager user (and the password works).

Then the negative cases:

- **Playing at 04:00.** Queue a long playlist before 04:00: the journal
  says "the room is playing; skipping tonight's update", nothing
  installs, the music never stops.
- **`auto_update=off`.** Set it on the card, boot, run
  `sudo systemctl start syncphony-update.service`: skipped.
  `sudo syncphony-box update` still updates.
- **Nothing new.** A second `sudo syncphony-box update`: `current`, no
  reboot.
- **No network.** Unplug the network and run
  `sudo systemctl start syncphony-update.service`: `result: failed` with
  apt's reason in `/v1/info`, no reboot, the box keeps playing its room
  once the network is back.
- **A kernel update.** When a release window includes a Raspberry Pi
  kernel update, check that it lands: `uname -r` after the reboot
  matches the newest `/lib/modules`, and the box still boots with the
  overlay (the initramfs was regenerated with the overlay script).
