// Package cec is boxd's HDMI-CEC half (MAD-807, ADR 0009). It talks to
// the kernel CEC framework through cec-ctl (v4l-utils) — not libcec —
// claims the playback logical address on the Pi's CEC adapters when boxd
// starts, watches the bus with `cec-ctl --monitor`, and does three things
// with what it sees:
//
//   - Wake and switch: on a playing bridge event, Image View On + Active
//     Source, so a TV in standby (or showing the game console) comes on
//     and shows the box. Rate-limited to once per playback start and at
//     most once every 5 minutes, so it never fights whoever holds the TV.
//   - Remote keys: CEC User Control Pressed (arrows, OK, Back, Play,
//     Pause) become Linux key events through uinput; cage hands them to
//     Chromium like any keyboard, and /tv already knows those keys. A
//     3-second hold of OK is the box's one remote gesture: it opens the
//     setup window and shows the box's IP and version.
//   - Optional standby (cec_standby=on, off by default): when the room
//     has been idle for cec_standby_minutes and the TV is showing the
//     box because the box switched it on — and nobody else has taken the
//     TV since (the monitor watches for foreign Active Source) — send
//     Standby.
//
// Everything hardware-facing is a func field, stubbed in tests, in the
// spirit of boxd's vcgencmd hook. A machine with no /dev/cec (a dev box,
// a Pi with CEC disabled) simply runs with all of this inert.
package cec

import (
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// wakeEvery rate-limits the wake-and-switch: however often playback
// starts, the TV is only seized once every 5 minutes, so someone who
// switched to the game console mid-party isn't fought for the input.
const wakeEvery = 5 * time.Minute

// Config is the CEC-relevant slice of syncphony.txt.
type Config struct {
	Enabled      bool          // cec=on|off
	SecondHDMI   bool          // audio=hdmi2: the TV is on /dev/cec1
	Standby      bool          // cec_standby=on|off (off by default)
	StandbyAfter time.Duration // cec_standby_minutes= (default 30m)
}

// Controller owns the adapters, the monitor processes and the uinput
// keyboard. Construct with New; Start claims the adapters and spawns the
// monitors; WakeAndSwitch and Housekeeping are called by the daemon.
type Controller struct {
	mu  sync.Mutex
	log *log.Logger
	cfg Config

	// The hardware edges, stubbed in tests.
	cecCtl       func(dev string, args ...string) (string, error)
	startMonitor func(dev string) (io.ReadCloser, func(), error)
	openKeydev   func() (keyDevice, error)
	listDevices  func() []string
	now          func() time.Time

	adapters  []*adapter
	keys      *remoteKeys
	lastWake  time.Time
	takenOver bool // the TV is on the box because the box switched it on
}

// adapter is one /dev/cecN: its device path and the physical address the
// kernel last reported for it (refreshed whenever the box transmits as
// active source — a hotplug or a different TV socket can change it).
type adapter struct {
	dev  string
	pa   string
	rc   io.ReadCloser
	stop func()
}

// New builds a controller. onLongOK fires when OK is held for 3 seconds;
// it is called from the monitor goroutine.
func New(cfg Config, lg *log.Logger, onLongOK func()) *Controller {
	if cfg.StandbyAfter <= 0 {
		cfg.StandbyAfter = 30 * time.Minute
	}
	c := &Controller{
		log: lg,
		cfg: cfg,
		cecCtl: func(dev string, args ...string) (string, error) {
			return runCecCtl(dev, args...)
		},
		startMonitor: realMonitor,
		openKeydev:   func() (keyDevice, error) { return openUinput() },
		listDevices:  realDevices,
		now:          time.Now,
	}
	c.keys = newRemoteKeys(onLongOK)
	return c
}

// realDevices lists the CEC devices this box has: /dev/cec0 always, and
// /dev/cec1 when the TV is on the second HDMI port (audio=hdmi2).
func realDevices() []string {
	devs := []string{"/dev/cec0"}
	if _, err := os.Stat("/dev/cec1"); err == nil {
		devs = append(devs, "/dev/cec1")
	}
	return devs
}

// Start registers the adapters as playback devices and starts their
// monitors, plus the uinput keyboard for the remote's keys. Devices that
// don't exist are skipped (with one log line); a box with none runs on
// without CEC. It reports how many adapters came up.
func (c *Controller) Start() int {
	if !c.cfg.Enabled {
		return 0
	}
	devs := c.listDevices()
	if !c.cfg.SecondHDMI {
		// The TV is on HDMI0; cec1 (if it exists) isn't ours to claim.
		devs = devs[:1]
	}
	started := 0
	for _, dev := range devs {
		if _, err := c.cecCtl(dev, "--playback"); err != nil {
			c.log.Printf("boxd: cec: %s: cannot register as a playback device: %v", dev, err)
			continue
		}
		a := &adapter{dev: dev}
		if out, err := c.cecCtl(dev); err == nil {
			a.pa = parsePhysAddr(out)
		}
		rc, stop, err := c.startMonitor(dev)
		if err != nil {
			c.log.Printf("boxd: cec: %s: cannot monitor: %v", dev, err)
			continue
		}
		a.rc, a.stop = rc, stop
		c.mu.Lock()
		c.adapters = append(c.adapters, a)
		c.mu.Unlock()
		go c.watchMonitor(a)
		started++
		c.log.Printf("boxd: cec: %s up (physical address %s)", dev, orUnknown(a.pa))
	}
	if started > 0 {
		if kd, err := c.openKeydev(); err != nil {
			c.log.Printf("boxd: cec: no uinput keyboard (/dev/uinput): %v — the TV remote's keys are off", err)
		} else {
			c.mu.Lock()
			c.keys.attach(kd)
			c.mu.Unlock()
		}
	}
	return started
}

// Close stops the monitors and releases the uinput device.
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.adapters {
		if a.stop != nil {
			a.stop()
		}
	}
	c.adapters = nil
	if c.keys != nil {
		c.keys.close()
	}
}

// activeDev is the adapter the TV is plugged into: /dev/cec1 for
// audio=hdmi2 when it came up, /dev/cec0 otherwise.
func (c *Controller) activeDev() *adapter {
	c.mu.Lock()
	defer c.mu.Unlock()
	var fallback *adapter
	for _, a := range c.adapters {
		if c.cfg.SecondHDMI && a.dev == "/dev/cec1" {
			return a
		}
		if a.dev == "/dev/cec0" {
			fallback = a
		}
	}
	return fallback
}

// WakeAndSwitch turns the TV on (Image View On) and switches it to the
// box (Active Source). Called once per playback start by the bridge; the
// rate limit here is the second lock — never more than once every
// wakeEvery — so the box doesn't fight whoever holds the TV.
func (c *Controller) WakeAndSwitch() {
	if !c.cfg.Enabled {
		return
	}
	a := c.activeDev()
	if a == nil {
		return
	}
	c.mu.Lock()
	if !c.lastWake.IsZero() && c.now().Sub(c.lastWake) < wakeEvery {
		c.mu.Unlock()
		return
	}
	c.lastWake = c.now()
	c.mu.Unlock()

	// A fresh read of the physical address: hotplugs and different TV
	// sockets change it, and an Active Source with a stale address does
	// nothing on some TVs.
	if out, err := c.cecCtl(a.dev); err == nil {
		if pa := parsePhysAddr(out); pa != "" {
			c.mu.Lock()
			a.pa = pa
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	pa := a.pa
	c.mu.Unlock()
	if pa == "" {
		c.log.Printf("boxd: cec: %s: no physical address; cannot switch the TV", a.dev)
		return
	}
	if _, err := c.cecCtl(a.dev, "--to=0", "--image-view-on"); err != nil {
		c.log.Printf("boxd: cec: %s: image-view-on: %v", a.dev, err)
	}
	if _, err := c.cecCtl(a.dev, "--to=0", "--active-source=phys-addr="+pa); err != nil {
		c.log.Printf("boxd: cec: %s: active-source %s: %v", a.dev, pa, err)
		return
	}
	c.mu.Lock()
	c.takenOver = true
	c.mu.Unlock()
	c.log.Printf("boxd: cec: woke the TV and switched it to this box (%s, %s)", a.dev, pa)
}

// Housekeeping runs the optional standby: cec_standby=on only, after the
// room has been idle for the configured minutes, and only while the TV
// is on the box because the box switched it on — a TV someone else took
// over (or that was never seized) is left alone.
func (c *Controller) Housekeeping(idleFor time.Duration) {
	if !c.cfg.Enabled || !c.cfg.Standby {
		return
	}
	c.mu.Lock()
	taken := c.takenOver
	c.mu.Unlock()
	if !taken || idleFor < c.cfg.StandbyAfter {
		return
	}
	a := c.activeDev()
	if a == nil {
		return
	}
	if _, err := c.cecCtl(a.dev, "--to=0", "--standby"); err != nil {
		c.log.Printf("boxd: cec: %s: standby: %v", a.dev, err)
		return
	}
	c.mu.Lock()
	c.takenOver = false
	c.mu.Unlock()
	c.log.Printf("boxd: cec: room idle for %s; TV to standby", idleFor.Round(time.Minute))
}

// watchMonitor reads one adapter's monitor output until it stops. A
// monitor that dies (a driver reload, a transient open) is restarted
// after a pause, for as long as the controller holds the adapter.
func (c *Controller) watchMonitor(a *adapter) {
	for {
		c.readMonitor(a)
		c.mu.Lock()
		held := c.holds(a)
		c.mu.Unlock()
		if !held {
			return
		}
		time.Sleep(10 * time.Second)
		rc, stop, err := c.startMonitor(a.dev)
		if err != nil {
			c.log.Printf("boxd: cec: %s: monitor restart failed: %v", a.dev, err)
			continue
		}
		c.mu.Lock()
		a.rc, a.stop = rc, stop
		c.mu.Unlock()
		c.log.Printf("boxd: cec: %s: monitor restarted", a.dev)
	}
}

func (c *Controller) holds(a *adapter) bool {
	for _, x := range c.adapters {
		if x == a {
			return true
		}
	}
	return false
}

// readMonitor parses one monitor run's lines into events.
func (c *Controller) readMonitor(a *adapter) {
	c.mu.Lock()
	rc := a.rc
	c.mu.Unlock()
	if rc == nil {
		return
	}
	p := &monitorParser{}
	readLines(rc, func(line string) {
		for _, ev := range p.line(line) {
			c.handleEvent(a, ev)
		}
	})
}

// handleEvent dispatches one parsed monitor event: remote keys to the
// uinput state machine, a foreign Active Source to the takeover flag.
func (c *Controller) handleEvent(a *adapter, ev monitorEvent) {
	switch ev.kind {
	case evKeyPress:
		c.mu.Lock()
		keys := c.keys
		c.mu.Unlock()
		if keys != nil {
			keys.press(ev.ui)
		}
	case evKeyRelease:
		c.mu.Lock()
		keys := c.keys
		c.mu.Unlock()
		if keys != nil {
			keys.release(ev.ui)
		}
	case evActiveSource:
		if !ev.received {
			return // our own Active Source announcement
		}
		c.mu.Lock()
		ours := a.pa
		c.mu.Unlock()
		if ours != "" && ev.pa != ours {
			c.takenOver = false
			c.log.Printf("boxd: cec: %s became the active source; leaving the TV alone", ev.pa)
		}
	}
}

// --- cec-ctl plumbing ---------------------------------------------------------

var physAddrRE = regexp.MustCompile(`Physical Address\s*:\s*([0-9a-f]+(?:\.[0-9a-f]+){3})`)

// parsePhysAddr reads the physical address out of cec-ctl's device info
// ("Physical Address : 1.0.0.0"). Empty means it wasn't there.
func parsePhysAddr(out string) string {
	if m := physAddrRE.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// parseUICmdHex parses a UI command code like "0x44".
func parseUICmdHex(s string) (byte, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 8)
	if err != nil {
		return 0, false
	}
	return byte(v), true
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
