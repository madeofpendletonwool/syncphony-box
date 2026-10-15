# Watchdogs and self-healing (MAD-806, ADR 0008). Two of the three
# watchdogs are image-level; the kiosk watchdog lives in boxd
# (boxd/internal/daemon/watchdog.go).
#
#   - Hardware watchdog: systemd's RuntimeWatchdogSec=15s arms whatever
#     /dev/watchdog the board has (the Pi's BCM watchdog, a PC's iTCO or
#     similar). The drop-in comes with the syncphony-box package
#     (/usr/lib/systemd/system.conf.d/50-syncphony-watchdog.conf). A
#     kernel or systemd hang reboots the box, the one failure no
#     userspace loop can catch.
#   - Time: systemd-timesyncd, enabled here and ordered before the kiosk
#     (time-sync.target in syncphony-kiosk.service). The server's
#     playback clock sync trusts the browser's clock, and a Pi has no RTC
#     battery by default; without sync the box boots with a stale clock.
#   - Thermals: boxd reads vcgencmd on a Pi and /sys/class/thermal
#     elsewhere, every minute; nothing to wire here.

systemctl enable systemd-timesyncd.service
