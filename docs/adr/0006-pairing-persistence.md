# ADR 0006: the box stays paired — profile durability

- Status: Accepted
- Date: 2026-10-14
- Deciders: Phase 11 plan (MAD-797), persistence issue (MAD-804)

## Context

Everything that makes a box "this room's screen and speaker" lives in
Chromium's profile directory:

- the `syncphony_display` cookie (HttpOnly, 180 days, sliding) that the
  server sets at pairing — the box's only credential;
- the `syncphony-tv-audio` localStorage flag that lets `/tv` resume the
  speaker role without a press (Syncphony MAD-802).

The box must keep both across reboots, power cuts at random moments and
Chromium crashes. Two facts make that non-trivial:

1. Chromium persists cookies and localStorage **lazily**: changes are
   committed to the profile's SQLite/LevelDB files asynchronously, and
   even committed writes sit in the kernel's dirty-page cache for up to
   ~30 s before reaching the SD card. A power pull seconds after pairing
   can therefore lose the cookie entirely.
2. After an unclean shutdown Chromium records
   `browser.exit_type = "Crashed"` / `browser.exited_cleanly = false` in
   the profile, which is what produces the "restore pages?" bubble on the
   next start.

## Decision

### One place defines the profile path

`config.DefaultProfileDir` is the only definition; the `SB_PROFILE` env
hook overrides it for dev and tests. `boxd config apply` writes it into
`config.env` as `PROFILE_DIR=`, the kiosk wrapper reads it from there
(asking `boxd profile path` when `config.env` is missing, with a
last-resort literal the smoke test pins to boxd's answer), and `sudo
syncphony-box reset` uses the same value. MAD-805 (stage 3, ADR 0007)
moved the profile to the `/data` partition — `/data/chromium` — by
changing exactly that constant.

### The bridge `paired` event triggers a sync burst

There is no public Chromium flag that makes cookie writes durable
(`--disable-features=…` has nothing to offer here; inventing a feature
name does nothing), so boxd does it from outside: on the ADR 0016
`paired` and `unpaired` events — the moments the cookie is written or
removed — boxd runs a `sync(2)` burst (immediately, then 5 s, 15 s and
30 s later, in a background goroutine). The first pass flushes what
Chromium has already committed; the later passes catch the commits
Chromium makes seconds after the event, because its cookie-store commit
is asynchronous. A `playing` start (when the speaker localStorage flag is
written) flushes the same way, rate-limited to once per 5 minutes so a
long night of music doesn't sync on every song. This covers the test the
issue names: pair, pull power within 10 s, boot — still paired.

Cookie *slides* don't need the burst: the token's value doesn't change
when the server refreshes the expiry, so losing a slide write leaves an
older-but-valid cookie on disk.

### Crash state is cleared before every start

The kiosk wrapper runs `boxd profile prep <dir>` immediately before
`exec`ing Chromium — every start, including systemd restarts after a
crash. Prep edits the profile's JSON in place, preserving every other
key:

- `Default/Preferences`: `browser.exit_type = "Normal"`
- `Local State`: `browser.exited_cleanly = true`

A missing file (fresh profile) is a no-op; invalid JSON is reported and
left alone, and prep never blocks the start (the wrapper runs it best
effort). Together with the existing `--disable-session-crashed-bubble`
flag this is belt and braces: even if one mechanism regresses, the TV
never shows a restore prompt.

### Reset: wipe the profile, show a new code

- **On the box:** `sudo syncphony-box reset` (installed at
  `/usr/bin/syncphony-box`) stops the kiosk, `boxd profile reset` removes
  the profile directory and syncs the deletion to disk, recreates the
  directory as the `kiosk` user, and restarts the kiosk — which then
  shows a fresh pairing code. Use it when a box moves to another room or
  another server.
- **In a dev checkout:** `make reset` runs the same `boxd profile reset`
  (SB_PROFILE points it at a dev profile).

`boxd profile reset` refuses empty, relative and `/` paths, and is a
no-op when the profile doesn't exist.

## Consequences

- The bridge's event handler now has a side effect beyond logging;
  stage 3's CEC and watchdog consume the same events, so the handler will
  grow a small dispatch — the flush stays one branch of it.
- `config parse` output gains a `PROFILE_DIR=` line (the bats suite pins
  presence of the other keys, not absence of new ones; the config.env
  contract for the wrapper is additive).
- `sync(2)` blocks for the length of the flush; on an idle Pi that is
  milliseconds, and it runs on a background goroutine so the bridge never
  waits for it.
- Hardware verification stays on the on-Pi checklist (README) and the
  power-pull gauntlet in docs/testing.md: pair → reboot → back in the
  room playing without a touch; pulls at random points after pairing; no
  restore prompt after any of them. With MAD-805 the profile sits on
  /data behind a journaled ext4, so the bursts flush to the one
  partition that persists.

## Considered and rejected

- **A Chromium `--disable-features=` option for durable cookies** — no
  such public feature exists; unknown feature names are silently ignored,
  which would be a false sense of security.
- **Reading/checkpointing the Cookies SQLite file from boxd directly** —
  touching Chromium's database from another process risks corruption for
  no gain over `sync()`, which reaches the same bytes through the page
  cache.
- **Restarting the kiosk on pairing to force a clean Chromium exit** —
  works, but spends seconds of black screen at the exact moment someone
  is standing in front of the TV with their phone; the sync burst gets
  the durability without the disruption.
