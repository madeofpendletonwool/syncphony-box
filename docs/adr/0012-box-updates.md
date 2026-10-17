# ADR 0012: Box updates

- Status: Accepted (overnight hardware run pending; see docs/testing.md)
- Date: 2026-10-15
- Deciders: Phase 11 plan (MAD-797), updates issue (MAD-810)

## Context

A box is flashed once and then lives behind a TV for years. The web app
updates itself because the Syncphony server serves it. But the OS (Debian
security updates) and the box's own software (boxd, the kiosk wrapper, the
units) were frozen at flash time. Two constraints shape how they can
update:

- **The root is read-only behind an overlay** (ADR 0007). A plain
  `apt upgrade` writes into the tmpfs upper layer: it looks applied, then
  vanishes at the next boot. Debian's own apt timers were disabled for
  exactly that reason.
- **Until now the image copied files in**, scattered over the pi-gen
  stages. There was no unit of "the box's software" that could be newer
  than what a box has.

Done when: a box flashed with release N, left on overnight after N+1 is
published, runs N+1 in the morning and is still paired.

## Decision

### The box's software is a package

Every file of the box's own (boxd, the kiosk wrapper, `syncphony-box`,
audio-setup, data-persist, every unit and drop-in, the initramfs overlay
scripts, the udev rule, the journald and watchdog drop-ins, the captive
portal's dnsmasq drop-in, the `syncphony.txt` template) moved from the
stages' `files/` directories to `packaging/root/`, laid out exactly as it
installs. `scripts/build-deb.sh` builds the `syncphony-box` .deb from that
tree plus a boxd binary. The maintainer script (`packaging/debian/postinst`)
does what the stages used to do with `on_chroot`: it creates the kiosk user
and the cec group and enables the units. It is idempotent, because it runs
on every update too.

- Units ship in `/usr/lib/systemd/system` (packages don't own
  `/etc/systemd/system`). Drop-ins, the udev rule and modules-load go in
  `/usr/lib` as well.
- Only the files with no `/usr` home stay in `/etc` (the initramfs
  scripts, the dnsmasq drop-in and the dpkg slim rules), as conffiles.
  Updates run with `--force-confold`, and nobody edits these by hand, so
  an update replaces them cleanly.
- A `triggers` file activates `update-initramfs`, so an update that
  changes the overlay script reaches the initramfs.

The image **installs the package** (`stage-syncphony/00-packages/01-run.sh`)
instead of copying files, so the image and the updates can't drift apart.
The stages keep only what a package can't own: the kernel command line,
`config.txt`, fstab, the initramfs module list, the links onto `/data`,
the kiosk user's PipeWire setup, and turning off Debian's apt timers.
That split is also the shared layer the x86-64 build (MAD-846) needs: the
package is distro-neutral and only the stages are pi-gen-specific.

Versions: tag `v1.3.0` builds `1.3.0`, and a build four commits past it is
`1.3.0+git4.gabc1234`. This sorts the way the releases do, so a dev box is
never "updated" back to the release it was cut after.

### The apt repo: GitHub Pages, signed, updater-only

The release job (ADR 0011) adds the .deb to an apt repo on the `gh-pages`
branch (`apt/`, one suite `stable`, keeping the last five versions per
architecture) and signs `Release` with an OpenPGP key. The private half
is the `APT_SIGNING_KEY` secret. The public half is committed
(`packaging/apt/syncphony-box-archive-keyring.asc`) and shipped by the
package itself in `/usr/share/keyrings`, so a key rotation reaches every
box as an ordinary update.

The repo's source line (`/usr/share/syncphony-box/apt/syncphony-box.list`)
is **not** in `/etc/apt/sources.list.d`. `boxd update` hands it to apt
with `-o Dir::Etc::SourceList=…`. On Pi OS the stock `sources.list` is
empty; Debian and Raspberry Pi live in `sources.list.d` and still apply.
The reason: pi-gen's export step runs `apt-get update && dist-upgrade`
on the image. If the box repo were configured, that would pull the
newest *published* package over the one the build just made, and it
would fail outright on a build before the repo exists.

### The nightly run: `boxd update`

`syncphony-update.timer` fires at 04:00 box-local time (Imager's timezone,
UTC otherwise; ten minutes of random spread; not `Persistent`, so a box
switched on at 09:00 doesn't start updating mid-party). It runs
`boxd update`:

1. `auto_update=off` in `syncphony.txt` → skip.
   Started outside the 04:00 hour → skip (manual runs exempt). That is a
   clock jump, not the schedule: a box without an RTC boots with an old
   clock, systemd plans the next 04:00 from it, and when NTP corrects the
   clock that 04:00 is already past, so the timer fires at once, in the
   middle of setup.
2. The room is playing → skip tonight. "Playing" is boxd's view: the
   page's last bridge event is `playing` **and** its heartbeat is fresh
   (a dead page's last word doesn't count). boxd exposes it on `/status`.
3. Under the overlay, every command runs on the real root through
   `syncphony-box chroot` (ADR 0007's maintenance path). The chroot now
   also brings `/data` (so the cloud-init and journal links resolve for
   package scripts) and a temporarily read-write `/boot/firmware` (a
   kernel or firmware update writes there), then puts it back to read-only.
4. Less than 150 MB free on the root → fail before touching anything (the
   root never grows; pi-gen leaves about 20% headroom).
5. `apt-get --error-on=any update`. A repo that can't be fetched or whose
   signature fails is a failure, not plain apt's warning-and-carry-on. Then
   `apt-get --with-new-pkgs upgrade`, so a new kernel or a new dependency of
   `syncphony-box` arrives as a new package. Then `apt-get clean`.
6. Compare `dpkg-query` before and after, and record the result in
   `/data/syncphony-box/update.json`: `updated`, `current` or `failed`,
   when, the installed `syncphony-box` version, what changed, and (for a
   failure) apt's own `E:`/`W:` lines.
7. Something changed → reboot as soon as the room is idle (polling every
   minute, for up to four hours; after that, the next boot picks it up).
   Nothing changed, or a failure → no reboot.

`sudo syncphony-box update` runs the same thing by hand (`--manual`: it
runs even with `auto_update=off` or music playing, but the reboot still
waits for a pause). `--no-reboot` leaves the reboot to the person. A lock
file keeps the timer and a manual run from overlapping.

`/v1/info` gains an `update` object (`at`, `result`, `error` if any) next
to the existing `version`. The room's Screens list shows it (MAD-811).

### Accounts survive updates

`data-persist restore` used to copy the saved account files over `/etc`
on every boot. An update that adds a system user or group to the real
root would have lost it at the next boot. `restore` now merges: saved
entries win (the Imager user and its password), and accounts that only the
image has are kept.

### Failure: keep it simple

A failed update records `failed`, never reboots, and retries the next
night. The running system is the old one: the overlay's lower layer may
have half-changed underneath, but nothing reloads it before a reboot. If
a box doesn't boot after an update, the documented fix is to reflash and
re-pair. A/B partitions are out of scope.

## Consequences

- Images built before this change have no repo and no updater; they need
  one reflash to join.
- A build without the repo key committed (a fork, a dev checkout) still
  builds and updates Debian packages, but never updates its own software.
  The release job refuses to publish such an image.
- `sudo syncphony-box chroot apt upgrade` by hand doesn't see the box
  repo. Use `sudo syncphony-box update`.
- Every box polls GitHub Pages and the Debian mirrors once a night.
