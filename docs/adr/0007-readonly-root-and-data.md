# ADR 0007: read-only root and the /data partition

- Status: Accepted
- Date: 2026-10-14
- Deciders: Phase 11 plan (MAD-797), read-only-root issue (MAD-805)

## Context

The box sits in a living room: people unplug it, the power blips, nobody
runs `shutdown`. ADR 0006 made the *pairing* durable; this ADR is about
the whole filesystem. A write-mounted ext4 on an SD card will eventually
be caught mid-write by a power pull, and a corrupted root means a box
that does not come back — the one failure mode the box cannot afford.

## Decision

### Three partitions; root is read-only behind an overlay

The image ships three partitions:

1. **boot** (FAT, as before) — kernel, firmware, `syncphony.txt`;
2. **root** (ext4) — the OS, mounted read-only at runtime;
3. **data** (ext4, label `syncphony-data`, 512 MiB in the image) —
   everything that must persist.

pi-gen's export stage can only lay out two partitions, so
`scripts/add-data-partition.sh` appends the third after export: it grows
the image file, `parted mkpart`s the partition, `mke2fs -d`s the
persistent layout into it (including the kiosk-owned Chromium profile
dir), and resolves the `DATADEV` placeholder in fstab to
`PARTUUID=<disk-id>-03` — the same disk-id spelling pi-gen's own
04-set-partuuid uses.

The overlay is exactly the mechanism `raspi-config`'s "Overlay FS" option
uses, enabled at build time instead of at runtime: an initramfs script at
`/etc/initramfs-tools/scripts/overlay` redefines `local_mount_root()` so
the real root is mounted read-only and overlaid with a tmpfs upper layer,
and the `boot=overlay` kernel command-line token activates it (the
initramfs `/init` sources `/scripts/$BOOT`). The command line is also
where the stock `resize` token comes *out* — see below. Our copy of the
script keeps the real root visible read-only at `/media/root-ro` for
maintenance, and mounts `/data` itself (after an `e2fsck -p`), so every
systemd unit — journald, cloud-init, the kiosk — sees the persistent
partition from the first moment.

`/boot/firmware` is mounted read-only too (`defaults,ro` in fstab). The
only runtime writes to it are boxd's own: `syncphony.txt` (the setup
form) and `cmdline.txt` (a changed `resolution=`/`rotate=`). Both go
through one remount dance — remount rw, write, sync, remount ro, and fail
safe either way — in `config.WithMountWritable`.

### /data holds everything that persists; one document lists it

The Chromium profile, the applied `config.env`, boxd's state, persistent
journald logs (capped at 50 MB), the machine-id, cloud-init's state, the
account files (the Imager user), SSH host keys, and Wi-Fi connections.
Most are symlinks into `/data`; the account files, SSH keys and
connections can't be (rewritten atomically, read too early), so
`data-persist` restores them from `/data` before cloud-init/sshd/
NetworkManager start and saves them back after the system settles plus
every 15 minutes. The complete, maintained list is
[docs/persistent-paths.md](../persistent-paths.md).

Two interactions the overlay forces, and how they're handled:

- **cloud-init.** With the overlay, `/etc/passwd` changes would vanish at
  the first reboot; with `/var/lib/cloud` persisted, cloud-init runs its
  first-boot modules exactly once and the identity restore/save pair keeps
  the resulting user. Without the pair, the box would lose SSH/console
  login after its first power cycle.
- **machine-id.** PID 1 binds it before any service runs, so a
  userspace-only restore would be too late: the initramfs overlay script
  seeds `/etc/machine-id` from `/data/system/machine-id`, and
  `data-persist save` writes the bound id back — the first boot's id
  becomes the box's permanent one.

### First boot grows /data, not root

The stock Trixie mechanism (`resize_early` in the initramfs +
`rpi-resize.service`) grows partition 2 — the root — which we never want
to grow. The `resize` cmdline token is removed at build time (leaving
both stock scripts inert; a package update reinstalling them changes
nothing), `rpi-resize.service` is disabled, and our own
`local-premount/grow-data` script — the same script, retargeted — grows
the **last** partition to fill the card. `syncphony-data-grow.service`
then runs the online `resize2fs` once (a `/data/system/.grown` marker
skips it afterwards; a reformatted `/data` loses the marker and grows
again).

### A broken /data never hangs the boot

`e2fsck -p` in the initramfs, `nofail` in fstab, and every service that
touches `/data` conditioned on it being mounted. If the partition can't
be read, the box boots with everything persistent on throwaway tmpfs:
unpaired, and with boxd's storage screen on the TV saying exactly that
instead of pretending.

### Maintenance writes the real root

The overlay makes on-box package updates an illusion that evaporates at
reboot, so the apt/unattended-upgrades timers are disabled. The supported
path is `sudo syncphony-box chroot [cmd]` (the overlayroot-chroot role):
it mounts the real root read-write at `/media/root-rw`, chroots with
`/dev` `/proc` `/sys` bound in, and changes take effect after a reboot.
The stage-4 updater builds on this.

## Consequences

- The box can be unplugged at any point — mid-boot, mid-playback, seconds
  after pairing — and comes back paired; the worst case is a replayed
  journal gap. The 50-pull test procedure is in
  [docs/testing.md](../testing.md).
- Anything that writes and isn't on the list in
  docs/persistent-paths.md is silently discarded at reboot. That is the
  feature; adding a persistent path is a deliberate act.
- The Chromium profile lives on a journaled ext4 mounted rw — it is the
  one thing still exposed to power pulls, mitigated by ADR 0006's sync
  bursts and by ext4 journaling itself.
- Root filesystem size is fixed at build time (plus pi-gen's margin);
  OS growth beyond that margin means rebuilding the image, which is the
  update model anyway (stage 4).
- Dev machines and tests never perform a remount dance: the SB_* hooks
  point at plain files, and `WithMountWritable` only dances when the
  target actually sits on a read-only mount.

## Considered and rejected

- **The `overlayroot` package / Ubuntu's tooling.** Raspberry Pi OS's own
  overlay mechanism (what raspi-config enables) is simpler, already in
  the initramfs, and needs no extra package; `syncphony-box chroot`
  covers the maintenance half that overlayroot-chroot would.
- **Keeping the stock `resize` token and shipping an fstab fixup.** The
  stock `set_partuuid` initramfs script rewrites the disk id and sed's
  fstab *through the overlay* on first boot — the edit lands in tmpfs and
  the box breaks on its second boot. Removing the token and growing a
  fixed root-free partition sidesteps all of it.
- **Bind-mounting /etc/passwd & friends from /data.** Atomic-replace
  writers (passwd, shadow, useradd) rename over the bind mount and fail
  with EBUSY; the restore/save pair keeps those tools working.
- **Read-write /boot/firmware.** The FAT has no journal at all; a power
  pull mid-write there is the nastiest corruption of the three
  partitions. Read-only with a remount dance for the two files boxd owns.
- **A squashfs root instead of an overlay.** More work to build and to
  update for the same guarantee; the overlay is the mechanism the OS
  already ships and documents.
