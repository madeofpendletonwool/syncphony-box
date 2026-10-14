package daemon

import (
	"strings"
	"testing"
	"time"
)

// healthyView is a configured box with everything fine: storage up,
// server answering, a page that beat 10 s ago and is idle, running for
// 10 minutes. Each test bends one input.
func healthyView(now time.Time) watchView {
	return watchView{
		now:        now,
		storageOK:  true,
		serverOK:   true,
		startedAt:  now.Add(-10 * time.Minute),
		lastBeat:   now.Add(-10 * time.Second),
		lastEvent:  "idle",
		localHour:  15,
		localDay:   now.Format("2006-01-02"),
		nightlyDay: "",
	}
}

// --- the heartbeat watchdog ---------------------------------------------------

func TestWatchdogRestartsStalledPage(t *testing.T) {
	now := time.Now()
	v := healthyView(now)
	v.lastBeat = now.Add(-2 * time.Minute) // stalled well past the window
	act := decide(v)
	if !act.restart || act.reboot || act.nightly {
		t.Fatalf("want a kiosk restart, got %+v", act)
	}
}

func TestWatchdogIgnoresStallWhenServerUnreachable(t *testing.T) {
	// A missing heartbeat with the server gone means "no /tv page" (the
	// offline screen has the TV) — not a stuck page. No restart, no loop.
	now := time.Now()
	v := healthyView(now)
	v.serverOK = false
	v.offlineSince = now.Add(-3 * time.Minute)
	v.lastBeat = now.Add(-2 * time.Minute)
	if act := decide(v); act.restart || act.reboot || act.nightly {
		t.Fatalf("stall must not trigger while the server is unreachable: %+v", act)
	}
}

func TestOfflineTriggerStillRestartsLivePage(t *testing.T) {
	// The MAD-803 behavior, unchanged: live page, gone server → the
	// offline screen takes over.
	now := time.Now()
	v := healthyView(now)
	v.serverOK = false
	v.offlineSince = now.Add(-3 * time.Minute)
	v.lastBeat = now.Add(-30 * time.Second)
	act := decide(v)
	if !act.restart {
		t.Fatalf("want the offline-screen restart, got %+v", act)
	}
}

func TestWatchdogHonorsSettleWindow(t *testing.T) {
	// A kiosk restarted a minute ago is still loading; even a stale
	// heartbeat must not restart it again yet.
	now := time.Now()
	v := healthyView(now)
	v.lastBeat = now.Add(-2 * time.Minute)
	v.lastRestart = now.Add(-1 * time.Minute)
	if act := decide(v); act.restart || act.reboot {
		t.Fatalf("restarted within the settle window: %+v", act)
	}
	v.lastRestart = now.Add(-5 * time.Minute)
	if act := decide(v); !act.restart {
		t.Fatalf("should restart after the settle window: %+v", act)
	}
}

func TestWatchdogFirstHeartbeatGrace(t *testing.T) {
	// A page that has never beaten gets the same grace after boot...
	now := time.Now()
	v := healthyView(now)
	v.lastBeat = time.Time{}
	v.startedAt = now.Add(-1 * time.Minute)
	if act := decide(v); act.restart || act.reboot {
		t.Fatalf("booted a minute ago, still loading: %+v", act)
	}
	// ...but a box up for ten minutes with no heartbeat ever is stuck.
	v.startedAt = now.Add(-10 * time.Minute)
	if act := decide(v); !act.restart {
		t.Fatalf("no heartbeat ever past the grace: %+v", act)
	}
}

func TestWatchdogSkipsWhenStorageLost(t *testing.T) {
	// The storage screen is terminal; rebooting can't fix the card, so
	// it never feeds the stall trigger.
	now := time.Now()
	v := healthyView(now)
	v.storageOK = false
	v.lastBeat = now.Add(-2 * time.Minute)
	if act := decide(v); act.restart || act.reboot || act.nightly {
		t.Fatalf("storage-lost box must not be watchdogged: %+v", act)
	}
}

func TestWatchdogRebootsInsteadOfFourthRestart(t *testing.T) {
	// Two watchdog restarts inside the window: the next stall is the
	// third — reboot rather than restart again.
	now := time.Now()
	v := healthyView(now)
	v.lastBeat = now.Add(-2 * time.Minute)
	v.restarts = []time.Time{now.Add(-7 * time.Minute), now.Add(-3 * time.Minute)}
	act := decide(v)
	if !act.reboot || act.restart {
		t.Fatalf("want a reboot on the third stall, got %+v", act)
	}
}

func TestWatchdogRestartsWhenHistoryAgedOut(t *testing.T) {
	// Only one of the two restarts is inside the 15-minute window: this
	// is the second, not the third — restart, don't reboot.
	now := time.Now()
	v := healthyView(now)
	v.lastBeat = now.Add(-2 * time.Minute)
	v.restarts = []time.Time{now.Add(-20 * time.Minute), now.Add(-3 * time.Minute)}
	if act := decide(v); !act.restart || act.reboot {
		t.Fatalf("want a restart, got %+v", act)
	}
}

// --- the nightly reload --------------------------------------------------------

func TestNightlyReloadAtFiveWhenIdle(t *testing.T) {
	now := time.Now()
	v := healthyView(now)
	v.startedAt = now.Add(-8 * time.Hour)
	v.localHour = nightlyReloadHour
	for _, event := range []string{"idle", "paused", ""} {
		v.lastEvent = event
		act := decide(v)
		if !act.nightly || act.restart || act.reboot {
			t.Fatalf("event %q: want the nightly reload, got %+v", event, act)
		}
	}
}

func TestNightlyReloadNeverWhilePlaying(t *testing.T) {
	now := time.Now()
	v := healthyView(now)
	v.startedAt = now.Add(-8 * time.Hour)
	v.localHour = nightlyReloadHour
	v.lastEvent = "playing"
	if act := decide(v); act.nightly || act.restart || act.reboot {
		t.Fatalf("must never reload while music is playing: %+v", act)
	}
}

func TestNightlyReloadOncePerDayAndOnlyAtFive(t *testing.T) {
	now := time.Now()
	v := healthyView(now)
	v.startedAt = now.Add(-8 * time.Hour)
	v.localHour = nightlyReloadHour
	v.nightlyDay = v.localDay // already done today
	if act := decide(v); act.nightly {
		t.Fatalf("already reloaded today: %+v", act)
	}
	v.nightlyDay = ""
	for _, hour := range []int{4, 6} {
		v.localHour = hour
		if act := decide(v); act.nightly {
			t.Fatalf("hour %d must not reload: %+v", hour, act)
		}
	}
}

func TestNightlyReloadOnlyForLongSessions(t *testing.T) {
	// A box booted at 04:50 doesn't need a 05:00 reload.
	now := time.Now()
	v := healthyView(now)
	v.localHour = nightlyReloadHour
	v.startedAt = now.Add(-1 * time.Hour)
	if act := decide(v); act.nightly {
		t.Fatalf("fresh boot must not reload: %+v", act)
	}
	v.startedAt = now.Add(-8 * time.Hour)
	if act := decide(v); !act.nightly {
		t.Fatalf("a long session should reload: %+v", act)
	}
}

// --- restartKiosk bookkeeping ---------------------------------------------------

func TestRestartKioskRecordsAndCounts(t *testing.T) {
	d, rec := newTestDaemon(t, "server_url=https://s.example\n")
	if err := d.restartKiosk("test", true); err != nil {
		t.Fatalf("restartKiosk: %v", err)
	}
	waitFor(t, func() bool {
		return strings.Contains(strings.Join(rec.calls(), ";"), "systemctl restart syncphony-kiosk.service")
	})
	d.mu.Lock()
	lastRestart, restarts := d.lastRestart, d.restarts
	d.mu.Unlock()
	if lastRestart.IsZero() || len(restarts) != 1 {
		t.Fatalf("bookkeeping: lastRestart=%v restarts=%v", lastRestart, restarts)
	}
	// An intentional restart settles but doesn't count.
	if err := d.restartKiosk("care", false); err != nil {
		t.Fatalf("restartKiosk: %v", err)
	}
	d.mu.Lock()
	restarts = d.restarts
	d.mu.Unlock()
	if len(restarts) != 1 {
		t.Fatalf("counted a care restart: %v", restarts)
	}
}
