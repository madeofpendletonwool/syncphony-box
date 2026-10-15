package daemon

import (
	"fmt"
	"time"
)

// The watchdog policy (MAD-806, ADR 0008): the box notices when it is
// stuck and fixes itself, without anyone walking over to it. Two
// independent watchdogs:
//
//   - The kernel's hardware watchdog, armed by systemd's
//     RuntimeWatchdogSec=15s (the package's system.conf.d drop-in), catches what
//     no userspace loop can: a kernel or PID-1 hang hard-resets the Pi
//     within 15 s.
//   - The loop here, in boxd, catches a wedged page: the /tv heartbeat
//     (ADR 0016, every 30 s while the page is healthy) stops for 90 s
//     while the server's /healthz still answers → restart
//     syncphony-kiosk.service. Chromium comes back, the page reloads, the
//     speaker resumes. If that happens 3 times in 15 minutes, the box
//     reboots — a kiosk boxd cannot keep alive is declared boot-sick.
//
// When the server is unreachable it is the offline screen's job, not the
// watchdog's: a missing heartbeat then means "there is no /tv page" (the
// kiosk sits on the local start screen), so the stall trigger requires
// /healthz to answer and the offline trigger keeps its own restart guard
// — no restart loops. The storage-lost screen is terminal the same way:
// rebooting cannot fix a dead /data, so it never feeds the stall trigger.
const (
	// heartbeatWindow is how long without a /v1/heartbeat counts as a
	// stalled page (three missed 30 s beats) — and, in the offline
	// trigger, how fresh a beat must be for the page to count as live.
	heartbeatWindow = 90 * time.Second

	// offlineGrace and kioskRestartGuard govern the offline screen
	// trigger: after the server has been unreachable this long while the
	// page is alive, restart the kiosk onto the local start page (the
	// offline screen), at most once per guard window. After the restart
	// there are no heartbeats (the page is local), so it cannot loop;
	// the guard is a second lock on that.
	offlineGrace      = 2 * time.Minute
	kioskRestartGuard = 10 * time.Minute

	// watchdogSettle is how long after a kiosk (re)start the stall
	// trigger stays quiet: cage, Chromium and the page need a minute or
	// two before their first heartbeat, and a fresh page must not be
	// restarted out from under itself. Short enough that three stalled
	// restarts still land inside watchdogWindow.
	watchdogSettle = 4 * time.Minute

	// watchdogRestarts kiosk restarts within watchdogWindow mean the box
	// cannot keep a kiosk alive: reboot instead of restarting again.
	watchdogRestarts = 3
	watchdogWindow   = 15 * time.Minute

	// nightlyReloadHour is the quiet hour, local time, at which the
	// kiosk is reloaded once a day: a long-lived Chromium with the
	// visualizer on slowly grows its heap, and a fresh page gives the
	// memory back. Only while the page is up and idle (never while
	// music is playing, per the bridge events), and only for a session
	// that has actually been running — a box booted at 04:50 does not
	// need a 05:00 reload.
	nightlyReloadHour = 5
	nightlyMinUptime  = 6 * time.Hour
)

// watchView is the locked snapshot the watchdog decision runs on — a
// plain struct so the policy is unit-testable without goroutines or
// clocks.
type watchView struct {
	now          time.Time
	storageOK    bool
	serverOK     bool
	startedAt    time.Time // boxd start; the first-heartbeat grace counts from here
	lastBeat     time.Time // zero = the page has never beaten
	lastEvent    string
	offlineSince time.Time
	lastRestart  time.Time
	restarts     []time.Time // watchdog-caused kiosk restarts inside watchdogWindow
	localHour    int
	localDay     string // "2006-01-02" in local time
	nightlyDay   string // the last day the nightly reload ran
}

// watchAction is what the policy decided for one tick.
type watchAction struct {
	restart bool // restart syncphony-kiosk.service (a symptom of a stuck box)
	nightly bool // restart the kiosk (care: a fresh page)
	reboot  bool
	reason  string
}

// decide is the whole watchdog policy, pure on its input (see the const
// block above for the reasoning). The watch loop applies the result.
func decide(v watchView) watchAction {
	if !v.serverOK {
		// The offline screen trigger: a live page whose server went
		// away — move the TV to the offline screen (the local start
		// page), guarded so it can never loop.
		if !v.offlineSince.IsZero() &&
			v.now.Sub(v.offlineSince) >= offlineGrace &&
			!v.lastBeat.IsZero() && v.now.Sub(v.lastBeat) < heartbeatWindow &&
			(v.lastRestart.IsZero() || v.now.Sub(v.lastRestart) > kioskRestartGuard) {
			return watchAction{
				restart: true,
				reason:  fmt.Sprintf("server unreachable for %s with a live page; moving to the offline screen", offlineGrace),
			}
		}
		return watchAction{}
	}

	// The heartbeat watchdog: a page that stopped beating (or never
	// started) while the server answers is stuck — unless the kiosk was
	// just (re)started and is still loading, or /data is gone (that
	// screen is terminal; a reboot cannot fix the card).
	settled := v.now.Sub(v.lastRestart) >= watchdogSettle &&
		v.now.Sub(v.startedAt) >= watchdogSettle
	stalled := !v.lastBeat.IsZero() && v.now.Sub(v.lastBeat) >= heartbeatWindow
	if v.storageOK && settled && (stalled || v.lastBeat.IsZero()) {
		// Only restarts inside the window count toward the reboot.
		recent := pruneWindow(v.restarts, v.now, watchdogWindow)
		if len(recent) >= watchdogRestarts-1 {
			return watchAction{
				reboot: true,
				reason: fmt.Sprintf("%d kiosk restarts within %s and the page is still stuck; rebooting",
					len(recent), watchdogWindow),
			}
		}
		return watchAction{restart: true, reason: "page heartbeats stopped while the server answers"}
	}

	// The nightly reload, at the quiet hour, only for a page that is up
	// and not playing. Skipped (not delayed) when a party runs through
	// the whole hour; there is always the next night.
	if v.localHour == nightlyReloadHour &&
		v.localDay != v.nightlyDay &&
		v.now.Sub(v.startedAt) >= nightlyMinUptime &&
		!v.lastBeat.IsZero() && v.now.Sub(v.lastBeat) < heartbeatWindow &&
		v.lastEvent != "playing" {
		return watchAction{nightly: true, reason: "nightly reload at the quiet hour"}
	}
	return watchAction{}
}

// watch is the background loop: sample the world, decide, act. Every 5 s
// is fine-grained enough for a 90 s heartbeat window.
func (d *Daemon) watch() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for range tick.C {
		if !d.configured() {
			// No server, no /tv: the setup screen is the state, not a
			// fault. (Thermals run in their own loop regardless.)
			continue
		}
		// CEC housekeeping (MAD-807): the optional good-night — a TV the
		// box switched on goes to standby once the room has been idle
		// for the configured minutes. Off unless cec_standby=on.
		d.mu.Lock()
		ev, evAt := d.lastEvent, d.lastEventAt
		d.mu.Unlock()
		if ev == "idle" && !evAt.IsZero() {
			d.cec.Housekeeping(time.Since(evAt))
		} else {
			d.cec.Housekeeping(0)
		}
		ok := d.serverReachable()
		now := time.Now()
		d.mu.Lock()
		if ok {
			d.offlineSince = time.Time{}
		} else if d.offlineSince.IsZero() {
			d.offlineSince = now
		}
		d.restarts = pruneWindow(d.restarts, now, watchdogWindow)
		day := now.Format("2006-01-02")
		act := decide(watchView{
			now:          now,
			storageOK:    storageHealthy(),
			serverOK:     ok,
			startedAt:    d.startedAt,
			lastBeat:     d.lastBeat,
			lastEvent:    d.lastEvent,
			offlineSince: d.offlineSince,
			lastRestart:  d.lastRestart,
			restarts:     d.restarts,
			localHour:    now.Hour(),
			localDay:     day,
			nightlyDay:   d.lastNightlyDay,
		})
		if act.nightly {
			// Marked under the same lock as the decision, so a slow
			// restart can't let the next tick fire it twice.
			d.lastNightlyDay = day
		}
		d.mu.Unlock()

		switch {
		case act.reboot:
			d.log.Printf("boxd: watchdog: %s", act.reason)
			if err := d.run("systemctl", "reboot"); err != nil {
				d.log.Printf("boxd: watchdog: reboot failed: %v", err)
			}
		case act.nightly:
			d.log.Printf("boxd: watchdog: %s", act.reason)
			d.restartKiosk("nightly reload", false)
		case act.restart:
			d.log.Printf("boxd: watchdog: %s", act.reason)
			d.restartKiosk(act.reason, true)
		}
	}
}

// restartKiosk restarts the kiosk unit and records the restart so the
// settle window applies to the fresh page too. count=true is for
// watchdog restarts — the symptoms the 3-in-15-minutes reboot counts;
// intentional restarts (remote reload, the setup save, the nightly
// reload) settle but don't count: they are care, not symptoms.
func (d *Daemon) restartKiosk(reason string, count bool) error {
	now := time.Now()
	d.mu.Lock()
	d.lastRestart = now
	if count {
		d.restarts = append(d.restarts, now)
	}
	d.mu.Unlock()
	d.log.Printf("boxd: restarting the kiosk (%s)", reason)
	if err := d.run("systemctl", "restart", "syncphony-kiosk.service"); err != nil {
		d.log.Printf("boxd: kiosk restart failed: %v", err)
		return err
	}
	return nil
}

// pruneWindow drops timestamps older than window: the restart history
// only ever answers "how many within the window".
func pruneWindow(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	kept := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	return kept
}
