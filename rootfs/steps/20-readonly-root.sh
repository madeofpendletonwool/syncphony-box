# Read-only root and /data (MAD-805, ADR 0007): make the box safe to pull
# the plug on. The platform-neutral half; the boot side (the kernel
# command line, the boot partition in fstab) is the wrappers' job:
# stage-syncphony/01-pi-boot on the Pi, the x86 installer on a PC.
#
#   - The overlay: raspi-config's "Overlay FS" mechanism, enabled at build
#     time. /etc/initramfs-tools/scripts/overlay (from the package), the
#     overlay module in the initramfs, and boot=overlay on the kernel
#     command line (the wrappers). The real root becomes a read-only lower
#     layer; runtime writes land in tmpfs.
#   - /data: a partition labelled syncphony-data holds everything that must
#     persist. The full list is docs/persistent-paths.md. Here we wire the
#     paths that reach it.
#
# The files (the initramfs overlay and grow-data scripts, data-persist,
# the /data units, the journald cap) come with the syncphony-box package,
# which also enables the units. This step makes the root-level changes a
# package can't own: the module list, fstab, and the paths that become
# links onto /data.

# --- the overlay -------------------------------------------------------------
# The overlay module must be in the initramfs (the script also has an
# insmod fallback off the real root, but don't rely on it). The initramfs
# itself is built after this (pi-gen's export runs update-initramfs; the
# x86 hook does it at the end), so the line lands inside it.
touch /etc/initramfs-tools/modules
grep -qx 'overlay' /etc/initramfs-tools/modules || echo overlay >> /etc/initramfs-tools/modules

# --- fstab --------------------------------------------------------------------
# /data with nofail: a box with a destroyed data partition still boots (the
# screen says the storage is lost) instead of hanging in emergency mode.
# DATADEV is a placeholder each platform resolves: the Pi to the third
# partition's PARTUUID (scripts/add-data-partition.sh), x86 to the label.
grep -q '[[:space:]]/data[[:space:]]' /etc/fstab ||
	printf 'DATADEV  /data  ext4  defaults,nofail,x-systemd.device-timeout=15s  0  2\n' >> /etc/fstab

# --- paths that live on /data now ---------------------------------------------
# Symlinks are the whole trick for most of them: read early, followed
# wherever the filesystem points, graceful when /data is missing. The
# account files, SSH keys and NetworkManager connections can't be symlinks
# (rewritten atomically, read too early). data-persist copies those.
install -v -d -m 0755 /data

rm -rf /var/log/journal
ln -v -sfn /data/journal /var/log/journal

rm -rf /var/lib/cloud
ln -v -sfn /data/system/cloud /var/lib/cloud

# config.env: boxd writes the real file on /data; the /etc path stays a
# symlink so the kiosk wrapper and the reset tool keep reading it there.
install -v -d -m 0755 /etc/syncphony-box
ln -v -sfn /data/syncphony-box/config.env /etc/syncphony-box/config.env

# SSH host keys: dangling until data-persist restore / ssh-keygen -A
# creates the targets on /data (through these symlinks).
install -v -d -m 0755 /etc/ssh
for type in rsa ecdsa ed25519; do
	for suffix in "" ".pub"; do
		key="ssh_host_${type}_key${suffix}"
		rm -f "/etc/ssh/${key}"
		ln -v -sfn "/data/system/ssh/${key}" "/etc/ssh/${key}"
	done
done

# --- units ----------------------------------------------------------------------
# Package downloads/upgrades onto a tmpfs overlay would vanish at reboot
# while looking applied. The one safe update path is writing the real
# root, which the box's own nightly update does (syncphony-update.timer
# runs boxd update through syncphony-box chroot; ADR 0012). Debian's
# timers stay off.
systemctl disable unattended-upgrades.timer apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true
