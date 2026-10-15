# ADR 0010: Wi-Fi setup hotspot

- Status: Accepted (hardware verification pending — see docs/testing.md)
- Date: 2026-10-15
- Deciders: Phase 11 plan (MAD-797), Wi-Fi setup issue (MAD-808)

## Context

The setup screen (ADR 0005) assumes the box is already on the network: a
phone on the same Wi-Fi scans the QR and sets the server. Raspberry Pi
Imager's Wi-Fi settings get a fresh card there, but a box flashed without
them, or moved to another house, has no network at all and nothing on
the TV can fix that. The box needs to set itself up with a phone and
nothing else.

## Decision

### The box becomes its own Wi-Fi network

If the box has no Ethernet and no Wi-Fi connection, boxd brings up an
open access point, `Syncphony-Box-XXXX` (the last four hex digits of the
Wi-Fi MAC, so two boxes in one house stay apart). NetworkManager runs it,
since it is the Pi OS default and already owns the radio. The connection
(`syncphony-setup`) is added with `save no`, so it exists only in memory.
It never reaches `/etc/NetworkManager/system-connections`, and so
data-persist never copies it to `/data`. Its settings are 2.4 GHz,
channel 6 (legal in every regulatory domain, including the "world"
domain of a box flashed without a Wi-Fi country), `ipv4.method shared`
at a fixed 10.42.0.1/24, and no IPv6. boxd turns the radio on
(`nmcli radio wifi on`) before using it, because a card flashed without
a Wi-Fi country starts with Wi-Fi soft-blocked.

The network is **open**. It only exists during setup, and it serves
nothing but the form below. A password printed on the TV would protect
nothing that "be in the room" doesn't already protect.

### When: one minute for a new box, five for one that was online

- A box that has **never been online** starts the hotspot when it has
  had no connection for **60 s** after boxd starts. That is enough time
  for Ethernet DHCP and an Imager-configured Wi-Fi to come up.
- A box that **has been online before** waits **5 minutes**, whether
  the network is missing at boot or drops mid-session. During that time
  the TV shows the offline screen. A router reboot must never put the
  box into setup mode. "Online before" is a marker file,
  `/data/boxd/network-seen`, written the first time a connection
  activates.
- "Online" means NetworkManager has an *activated* Ethernet or Wi-Fi
  connection (link up and addressed) other than the access point.
- The hotspot **goes away** once the box joins a network from the form,
  or when Ethernet comes up while it is running.
- With the access point up, the radio cannot look for the house network.
  So if a Wi-Fi network is saved, boxd releases the radio every
  **5 minutes**, for up to 45 s, to let NetworkManager find it (the
  router may simply have come back). It only does this when nobody has
  used the portal for 2 minutes, so a phone in the middle of the form is
  never dropped.

The policy is one pure function (`decideNet`), the same pattern as the
watchdog's `decide`, and it is pinned by a table test.

### The captive portal

NetworkManager's shared mode serves DHCP and DNS on the access point
through its own dnsmasq, and it reads `/etc/NetworkManager/dnsmasq-shared.d`.
The image's drop-in there resolves **every name to 10.42.0.1** and sends
DHCP option 114 (RFC 8910, the captive portal URI). While the hotspot is
up, boxd's existing LAN listener on `:80` is the portal. Every request
not addressed to `10.42.0.1/` is redirected there. That covers the
phones' connectivity probes (`generate_204`, `hotspot-detect.html`),
which is why the phone opens the setup page by itself.

The form lists the networks from a scan taken just before the access
point came up (the radio is busy afterwards). It also has a field for a
network that isn't listed (joined as hidden), a password field, and,
only when the box has no `server_url` yet, the server URL and the box's
name. Passwords for listed secured networks are checked for presence and
length before anything changes, so the phone gets those errors while it
is still connected.

### The TV

`/hotspot` is a new kiosk screen. It shows the join QR code with the
standard Wi-Fi payload (`WIFI:S:Syncphony-Box-XXXX;T:nopass;;`, so the
phone camera offers to join), "a setup page opens", and `http://10.42.0.1`
as a fallback. `/setup` and `/start` hand over to it whenever the
hotspot is running, and it hands back to `/start` when the hotspot ends.
The setup screen used to be static. It now polls `/status` to make those
handovers. `/status` gains `wifiSetup` and a `wifi` object with the
phase, the access point's name, the network being joined, and the last
error.

### The TV's own controls

Added after the first PC run (ADR 0013), where the setup screen said
"in a minute" and then, with the hotspot failing to start, nothing at
all. A box with a mouse or a keyboard (a PC, or a Pi with one plugged
in), or a remote for the buttons, can do more than wait:

- **The countdown.** `/status`'s `wifi` object gains `hotspotIn` (seconds
  until the hotspot starts, while the box is offline and waiting),
  `device` and `unavailable` (no Wi-Fi device, and nmcli's reason), and
  `startError` (why the last start failed; the grace then re-arms). The
  setup screen shows all of it instead of a fixed "in a minute".
- **Start setup Wi-Fi now.** A button on the setup screen
  (`POST /wifi/hotspot`): the hotspot without the grace, for an offline
  box with a Wi-Fi device.
- **Choose Wi-Fi on this screen.** `/wifi`, linked from the setup and
  hotspot screens, lists the networks from a fresh scan
  (`POST /wifi/scan`) and joins one (`POST /wifi/join`) with the
  portal's checks. A scan needs the radio, so a running hotspot gives it
  up. While the screen is in use it pings `POST /wifi/hold`, and the
  policy doesn't start the hotspot within 30 s of the last ping; after
  three idle minutes the screen goes back to `/setup` and the usual
  timing resumes. A failed join from the TV shows its reason there and
  doesn't bring the hotspot up under it.

These routes are on the loopback listener only, and refuse any Origin
but boxd's own, so neither the server's page nor anything else can
drive the radio. The remote's arrows move the focus between the
buttons; typing a password needs a keyboard.

### Saving: answer first, then join

The access point and the house network share one radio. So boxd answers
the phone first ("Connecting to Home — your phone will leave this
network"), then takes the access point down and joins with
`nmcli device wifi connect … --wait 45`. The connection is named after
the network, so it autoconnects from then on.

- **Success:** boxd runs `data-persist save` immediately, rather than
  waiting for the 15-minute timer, so a power pull right after setup
  keeps the network. If the form included a server URL, boxd checks its
  `/healthz` (retrying for about 30 s while DHCP, DNS and the clock
  settle). If it answers, boxd saves it through the same path as the
  LAN form, and the TV goes on to pairing. If it doesn't answer, the URL
  is not saved (the same rule as ADR 0005), and the setup screen says
  "Joined Home, but couldn't reach … — set the server from a phone on
  Home".
- **Failure:** boxd deletes the half-created connection, brings the
  access point back with a fresh scan, and shows the reason on the TV
  ("Couldn't join Home — is the password right?"). The phone sees the
  same message on the form when it rejoins.

## Consequences

- `network-manager` and `dnsmasq-base` are explicit image packages, and
  boxd is ordered after NetworkManager (stage-syncphony/09-wifi-setup).
- Imager's Wi-Fi settings still work and are still the fast path. The
  hotspot is the fallback for a box that was moved, or flashed without
  them.
- With no NetworkManager or no Wi-Fi device (a dev machine, or a
  Pi without Wi-Fi), the hotspot logs once and stays inert.
- An open setup network means a neighbor within Wi-Fi range could, in
  the window when a box is unconfigured *and* offline, point it at their
  own Wi-Fi. They would gain a TV kiosk that shows a server URL they
  choose, which the owner fixes the same way, from the TV. This is
  accepted, as with the unconfigured LAN form in ADR 0005.
- What CI can't cover: real phones' captive-portal behavior (iOS,
  Android), the Pi's brcmfmac access point, and the end-to-end setup.
  The procedure is in docs/testing.md and is still pending a run.
