#!/bin/bash -e

# The Wi-Fi setup hotspot (MAD-808, ADR 0010): a box with no Ethernet and
# no Wi-Fi becomes its own open network, Syncphony-Box-XXXX, so a phone
# can give it the house Wi-Fi. The image-level pieces:
#
#   - NetworkManager + dnsmasq-base (00-packages): the access point runs
#     in NetworkManager's shared mode, which serves DHCP and DNS through
#     dnsmasq.
#   - The dnsmasq-shared.d drop-in (on that network every name resolves
#     to the box and DHCP names the portal — the captive portal) and the
#     boxd drop-in ordering it after NetworkManager both come with the
#     syncphony-box package.
#
# boxd itself (when to start the hotspot, the portal form, the join, the
# TV screen) lives in boxd/internal/daemon/hotspot.go and
# boxd/internal/wifi. The access point's connection is created in memory
# only, so it never lands in system-connections — or on /data.

on_chroot << EOF
systemctl enable NetworkManager.service
EOF
