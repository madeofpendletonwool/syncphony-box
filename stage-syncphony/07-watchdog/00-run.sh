#!/bin/bash -e

# Watchdogs and self-healing (MAD-806, ADR 0008). Two of the three
# watchdogs are image-level; the kiosk watchdog lives in boxd
# (boxd/internal/daemon/watchdog.go).
#
#   - Hardware watchdog: systemd's RuntimeWatchdogSec=15s arms the BCM
#     watchdog. The drop-in comes with the syncphony-box package
#     (/usr/lib/systemd/system.conf.d/50-syncphony-watchdog.conf). A
#     kernel or systemd hang reboots the Pi — the one failure no
#     userspace loop can catch.
#   - Time: systemd-timesyncd, enabled here and ordered before the kiosk
#     (time-sync.target in syncphony-kiosk.service). The server's
#     playback clock sync trusts the browser's clock, and a Pi has no RTC
#     battery by default — without sync the box boots with a stale clock.
#   - Thermals: boxd runs vcgencmd (installed with this stage's packages)
#     every minute; nothing to wire here.

on_chroot << EOF
systemctl enable systemd-timesyncd.service
EOF
