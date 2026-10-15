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
- The Wi-Fi setup hotspot for a box with no network.
- HDMI-CEC: the TV wakes and switches when music starts; the remote's
  keys drive the box; optional standby when the room is idle.
- Watchdogs and self-healing: hardware watchdog, kiosk watchdog,
  thermals in the journal, a nightly kiosk reload.
- Read-only root and the `/data` partition: safe to unplug at any time.
- boxd: the box bridge, the setup and offline screens, pairing that
  survives reboots and power cuts.
- Audio output selection (`audio=`) on PipeWire.
- `syncphony.txt` boot config file and the cage + Chromium kiosk session.

### Changed
- The image is smaller: documentation, man pages and translations are
  stripped (copyright files stay).
- The Pi image and the x86 ISO are built from one shared customisation
  layer (`rootfs/`), so they can't drift apart.

## [0.1.0]

### Added
- The pi-gen build for the syncphony-box image.
