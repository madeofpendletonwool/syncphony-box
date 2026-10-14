# Testing the box on hardware

CI can't boot a Pi. The unit tests (bats + Go, `make test`) and the image
smoke test (`scripts/smoke-test.sh`, runs in CI on every build) pin the
image's structure; the checks below are for real hardware. The README's
on-Pi checklist covers the everyday list — this page is the destructive
one.

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
