# Changelog

Every release's notes come from its section here: pushing tag `vX.Y.Z`
publishes the `## [X.Y.Z]` section as the GitHub Release notes, and the
release fails if the section is missing ([docs/releasing.md](docs/releasing.md)).
Add to `## [Unreleased]` as you go; rename it when you tag.

## [Unreleased]

### Added
- **The box on any x86-64 PC.** Each release also has
  `syncphony-box-<version>-amd64.iso`, a hybrid ISO for a USB stick or
  DVD, UEFI or legacy BIOS (Secure Boot off). Boot **Syncphony Box** to
  run the box from the stick (a `syncphony-data` partition on the stick
  keeps its pairing), or **Install Syncphony Box to disk** to install it,
  with the same read-only root, `/data`, setup and nightly updates as a
  Pi. Thermals come from the PC's thermal zones, `audio=` picks a PC's
  HDMI/DisplayPort or analog outputs, and there's no HDMI-CEC.
- **Releases you can flash from Raspberry Pi Imager.** A `v*` tag builds
  the image, smoke-tests it and publishes a GitHub Release with the
  `.img.xz`, its checksum, a cosign-signed `SHA256SUMS` and the notes
  from this file. An Imager listing (`imager/os_list.json` on GitHub
  Pages) puts "Syncphony Box" into Imager through App Options → Content
  Repository, with Imager's Wi-Fi, SSH and hostname settings working.
- **Updates without reflashing.** The box's own software is now the
  `syncphony-box` .deb, served from a signed apt repository on GitHub
  Pages. Every night around 04:00 the box installs Debian security
  updates and new box releases onto its read-only root, never while
  music plays, and reboots only if something changed and the room is
  idle. `auto_update=off` in `syncphony.txt` turns it off;
  `sudo syncphony-box update` runs it by hand.
- `/v1/info` reports uptime and the last update (when, and whether it worked) for the
  room's Screens list.
- The Wi-Fi setup hotspot for a box with no network. The setup screen
  counts down to it, says why when it can't start, and has a **Start
  setup Wi-Fi now** button. **Choose Wi-Fi on this screen** sets the
  Wi-Fi from the TV with a keyboard, no phone needed.
- HDMI-CEC: the TV wakes and switches when music starts; the remote's
  keys drive the box; optional standby when the room is idle.
- Watchdogs and self-healing: hardware watchdog, kiosk watchdog,
  thermals in the journal, a nightly kiosk reload.
- Read-only root and the `/data` partition: safe to unplug at any time.
- boxd: the box bridge, the setup and offline screens, pairing that
  survives reboots and power cuts.
- Audio output selection (`audio=`) on PipeWire.
- `syncphony.txt` boot config file and the cage + Chromium kiosk session.

### Fixed
- x86 live session: booting from a stick without a `syncphony-data`
  partition no longer stalls on a missing `/data` device and then fails
  SSH and the kiosk (`mkdir: cannot create directory '/data/chromium'`).
  `/data` is the stick's partition when there is one, and RAM otherwise.
- x86: the setup hotspot never appeared, because the image had no
  `wpasupplicant` (NetworkManager only recommends it), so NetworkManager
  couldn't scan or run an access point. It also gains `wireless-regdb`
  and MediaTek and Broadcom Wi-Fi firmware.
- After setup gave the box a name, the TV could show Chromium's "The
  profile appears to be in use by another Chromium process on another
  computer" instead of the room. The rename changed the hostname, and
  Chromium wouldn't take over the lock its killed predecessor left
  behind. The kiosk now clears that stale lock before every start (the
  same after a power cut).
- A Wi-Fi network joined from the setup hotspot was forgotten at every
  reboot, so the box came back "offline" and asked for Wi-Fi again.
  `data-persist` copied the SSH host keys onto themselves (they are
  symlinks into `/data`), failed, and stopped before saving or restoring
  the NetworkManager connections.
- The box's hostname is now in `/etc/hosts` after a rename (`sudo` said
  "unable to resolve host" on every call).
- Ctrl+Alt+F1–F12 now reach a console login when a keyboard is plugged
  in; cage blocked VT switching.
- A kiosk that fails several times quickly keeps being restarted instead
  of leaving the TV on a dead console.
- A box with no clock battery could start the nightly update the moment
  it first got online (the clock correcting itself made the timer fire).
  Timer runs outside the 04:00 hour are skipped.
- The kiosk restarted every few minutes on a server reached over the
  internet. Chromium 142+ asks permission before a public site may call
  the box's helper on `127.0.0.1`, nobody can answer that in a kiosk, so
  the page's heartbeats never arrived and the watchdog took it for stuck.
  The box now sets a Chromium policy allowing its configured server.
- On a PC, audio went to the first HDMI/DisplayPort output rather than
  the one the TV is on (a laptop has one per USB-C port), so the TV was
  silent. It now picks the output with a display plugged in, switching
  the sound card's profile to it when needed.
- After joining Wi-Fi from the setup hotspot, the server was often not
  saved ("couldn't reach"), because the box's clock was still months
  behind and https failed. The box now asks NTP right after joining and
  checks the server for two minutes, and it keeps its last known time
  on `/data`, so it no longer boots at the image's build date.
- Joining a network right after the setup hotspot closed could fail with
  "couldn't find <network>" because the scan hadn't finished; the join
  now waits for the network to show up.
- Wi-Fi power saving is off: some cards (MediaTek mt7921) stopped
  answering ping and SSH while the box was idle.

### Changed
- The image is smaller: documentation, man pages and translations are
  stripped (copyright files stay).
- The Pi image and the x86 ISO are built from one shared customisation
  layer (`rootfs/`), so they can't drift apart.

## [0.1.0]

### Added
- The pi-gen build for the syncphony-box image.
