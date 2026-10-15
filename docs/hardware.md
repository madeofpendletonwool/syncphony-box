# Hardware: boards, TVs and CEC

What the project has verified on real hardware, and what to check when
you add a piece of your own. CEC is the uneven one: every brand ships
its own subset (and its own menu name for it), so the table below is the
record the CEC issue (MAD-807, ADR 0009) asked for.

## Boards

| board | status | notes |
|---|---|---|
| Pi 5 | pending bench | HDMI0 is the port next to the USB-C power input; that port is `cec0` |
| Pi 4 (2 GB+) | pending bench | HDMI0 is next to the power input too; the 3.5 mm jack makes `audio=analog` possible |

| x86-64 PC (ISO, ADR 0013) | pending bench | VM-tested procedure in docs/testing.md; no CEC (PC GPUs can't send it); `audio=hdmi` is the first HDMI/DisplayPort output |

Both Pi ports: plugging into HDMI1 instead of HDMI0 means `audio=hdmi2` in
`syncphony.txt` (the box then claims `/dev/cec1`). Power: use the
official power supply — under-voltage shows up as audio dropouts long
before anything else (the watchdog's thermals log flags it; see ADR
0008).

## TV CEC compatibility

The done-when for MAD-807: with the TV in standby, queueing a song from
a phone turns the TV on, switches it to the box and plays; the remote's
OK takes over as speaker; play/pause works. Rows fill in as benches
run; **the table starts pending** — no brand has been verified yet.

| TV brand (CEC name) | wake + switch | remote keys | OK long-press | standby | notes |
|---|---|---|---|---|---|
| Samsung (Anynet+) | pending | pending | pending | pending | Anynet+ often needs "Auto Turn On"/"Auto Power Off" enabled in the TV's Device Connector settings |
| LG (SimpLink) | pending | pending | pending | pending | SimpLink must be on; some LG remotes send OK as 0x00 only after a first pairing |
| Sony (Bravia Sync) | pending | pending | pending | pending | usually the most complete subset; control mode must be set to "Normal" not "Off" |
| (yours here) | | | | | file a hardware report (issue template) with the TV model |

### Checking a TV in five minutes

```sh
sudo apt install v4l-utils          # if it isn't already
cec-ctl -d /dev/cec0                 # note the "Physical Address" line (e.g. 1.0.0.0)
cec-ctl -d /dev/cec0 --playback -S   # claim playback, show the topology
# TV off / on another input — use the physical address just shown:
cec-ctl -d /dev/cec0 --to 0 --image-view-on
cec-ctl -d /dev/cec0 --to 0 --active-source phys-addr=1.0.0.0
cec-ctl -d /dev/cec0 --monitor      # then press keys on the TV remote
```

If `--monitor` shows `USER_CONTROL_PRESSED` lines when you press arrow
keys and OK, the box will drive `/tv` on this TV. If nothing appears,
the TV is probably sending its keys only to its own logical address —
check the TV's CEC menu first, then record it here either way.
