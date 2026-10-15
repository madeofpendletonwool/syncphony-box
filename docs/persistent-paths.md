# Persistent paths on the box

The root filesystem is read-only behind an overlay (ADR 0007): everything
written under `/` at runtime lands in a tmpfs and vanishes at power-off.
Anything that must survive lives on `/data`, the third partition (label
`syncphony-data`). This page is the complete list — when a path is added
to or moved off `/data`, this list changes in the same commit.

| path | lives at | put there by | what it is |
|---|---|---|---|
| Chromium profile (pairing cookie, speaker flag) | `/data/chromium` | kiosk user; path pinned by boxd's `config.DefaultProfileDir` | the box's whole identity — the room pairing |
| applied config | `/data/syncphony-box/config.env` | `boxd config apply` / the setup form | parsed `syncphony.txt`; `/etc/syncphony-box/config.env` is a symlink to it |
| last update result | `/data/syncphony-box/update.json` | `boxd update` (the nightly timer, or `sudo syncphony-box update`) | when the last update ran, `updated`/`current`/`failed`, the installed version; `/v1/info` reports it (ADR 0012) |
| boxd's own state | `/data/boxd` | boxd (as needed) | daemon state; today `network-seen`, the marker that this box has been online once (the setup hotspot then waits 5 minutes, not 1 — ADR 0010) |
| journald logs | `/data/journal` | journald (`/var/log/journal` → symlink) | persistent logs, capped at 50 MB (`SystemMaxUse=50M`) |
| machine identity | `/data/system/machine-id` | `data-persist save` (the bound id) | seeded into `/etc/machine-id` by the initramfs overlay script |
| cloud-init state | `/data/system/cloud` | cloud-init (`/var/lib/cloud` → symlink) | without it cloud-init would re-run its first boot every boot |
| account files | `/data/system/identity/{passwd,shadow,group,gshadow,subuid,subgid}` | `data-persist save` → restored by `data-persist restore` | the Imager-created user and password; without this they would vanish with the overlay |
| SSH host keys | `/data/system/ssh/` | `ssh-keygen -A` at restore, saved back by `save` | `/etc/ssh/ssh_host_*` are symlinks here; keeps `known_hosts` stable |
| network connections | `/data/system/network/` | `data-persist save` → restored by `restore`; boxd runs a save right after the setup hotspot joins a network | NetworkManager `system-connections` (Wi-Fi credentials). The hotspot's own access point is in-memory only and never lands here |

Not on `/data`, still persistent:

| path | where | what |
|---|---|---|
| `syncphony.txt` | the FAT boot partition (`/boot/firmware`) | the box's config, editable from any computer; written only through the remount dance (rw → write → sync → ro) |

Everything else is deliberately throwaway: apt state, Chromium cache,
`$HOME`, `systemd`'s runtime state. The overlay discarding them is the
feature — the SD card can be yanked at any moment without corrupting
anything that matters.

## If /data can't mount

The initramfs fsck's `/data` (`e2fsck -p`) and mounts it only when it's
clean enough. If it isn't, the box boots anyway: the pairing is lost, the
kiosk shows the storage-lost screen, and everything above runs on tmpfs
for that boot. The fix is at the card (check, re-flash, or replace it) —
nothing on the TV is recoverable.
