package cec

import (
	"sync"
	"time"
)

// The remote's keys. CEC User Control Pressed/Released arrive as pairs;
// they become Linux key events through the injected uinput keyboard,
// which cage passes to Chromium like any keyboard. /tv already knows the
// keys (tvKeyOf in the web app): Enter clicks, arrows steer, Escape
// leaves, MediaPlayPause plays and pauses.
//
// Linux key codes from linux/input-event-codes.h.
const (
	keyEsc       = 1
	keyEnter     = 28
	keyUp        = 103
	keyLeft      = 105
	keyRight     = 106
	keyDown      = 108
	keyPlayPause = 164
)

// CEC UI command codes (HDMI CEC 1.4 table "UI Command codes") for the
// keys /tv uses. Everything else on the remote is ignored — number keys,
// colored keys, volume (the TV keeps its own volume, as it should).
const (
	uiSelect = 0x00
	uiUp     = 0x01
	uiDown   = 0x02
	uiLeft   = 0x03
	uiRight  = 0x04
	uiExit   = 0x0d
	uiPlay   = 0x44
	uiPause  = 0x46
)

// mapKey maps a CEC UI command to the Linux key event it becomes.
func mapKey(ui byte) (uint16, bool) {
	switch ui {
	case uiSelect:
		return keyEnter, true
	case uiUp:
		return keyUp, true
	case uiDown:
		return keyDown, true
	case uiLeft:
		return keyLeft, true
	case uiRight:
		return keyRight, true
	case uiExit:
		return keyEsc, true
	case uiPlay, uiPause:
		return keyPlayPause, true
	}
	return 0, false
}

// Vars so the tests can shorten the waits; the values are the product.
var (
	// longOKDelay: OK held this long is the box gesture, not a click.
	longOKDelay = 3 * time.Second
	// keyReleaseGuard: a key whose User Control Released never arrives
	// (some TVs and repeaters lose them) is released anyway, so nothing
	// sticks down in Chromium.
	keyReleaseGuard = time.Second
)

// remoteKeys is the press/release state machine between the monitor and
// the uinput keyboard.
//
// OK is special (hold-to-gesture): the Enter keydown is held back until
// the release says it was a short press — otherwise the 3-second hold
// would click whatever is focused on /tv before the gesture fires. A
// hold fires onLongOK instead, and the eventual release is swallowed.
type remoteKeys struct {
	mu       sync.Mutex
	inject   func(code uint16, down bool)
	onLongOK func()
	timers   map[byte]*time.Timer
	forced   map[byte]bool // the release guard already released this key
	okLong   bool          // OK is past the long-press point
	dev      keyDevice
}

func newRemoteKeys(onLongOK func()) *remoteKeys {
	return &remoteKeys{
		onLongOK: onLongOK,
		timers:   make(map[byte]*time.Timer),
		forced:   make(map[byte]bool),
	}
}

// attach wires a real uinput keyboard (Start calls it once the adapters
// are up; until then presses are counted but go nowhere).
func (r *remoteKeys) attach(dev keyDevice) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dev = dev
	r.inject = func(code uint16, down bool) {
		if down {
			dev.Press(code) //nolint:errcheck — logged in the uinput layer
		} else {
			dev.Release(code) //nolint:errcheck
		}
	}
}

// close releases the uinput device.
func (r *remoteKeys) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.timers {
		t.Stop()
	}
	r.timers = nil
	if r.dev != nil {
		r.dev.Close() //nolint:errcheck
		r.dev = nil
	}
}

func (r *remoteKeys) press(ui byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timers == nil {
		return // closed
	}
	if ui == uiSelect {
		// Hold back Enter; a short release clicks, a 3 s hold gestures.
		// Some TVs keep sending User Control Pressed while OK is held —
		// the repeats are ignored so the hold can mature.
		if r.timers[ui] != nil {
			return
		}
		r.okLong = false
		r.timers[ui] = time.AfterFunc(longOKDelay, func() {
			r.mu.Lock()
			r.okLong = true
			onLongOK := r.onLongOK
			r.mu.Unlock()
			if onLongOK != nil {
				onLongOK()
			}
		})
		return
	}
	code, ok := mapKey(ui)
	if !ok {
		return
	}
	if r.inject != nil {
		r.inject(code, true)
	}
	r.forced[ui] = false
	// No User Control Released within the guard → release anyway; the
	// late release, if it ever comes, is then a no-op.
	r.timers[ui] = time.AfterFunc(keyReleaseGuard, func() {
		r.mu.Lock()
		r.forced[ui] = true
		inject := r.inject
		r.mu.Unlock()
		if inject != nil {
			inject(code, false)
		}
	})
}

func (r *remoteKeys) release(ui byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timers == nil {
		return
	}
	if t := r.timers[ui]; t != nil {
		t.Stop()
		delete(r.timers, ui)
	}
	if ui == uiSelect {
		if r.okLong { // the gesture already happened
			r.okLong = false
			return
		}
		if r.inject != nil { // a short press: click now
			r.inject(keyEnter, true)
			r.inject(keyEnter, false)
		}
		return
	}
	if r.forced[ui] { // the guard beat the release to it
		delete(r.forced, ui)
		return
	}
	if code, ok := mapKey(ui); ok && r.inject != nil {
		r.inject(code, false)
	}
}

// setInject overrides the injection edge (tests).
func (r *remoteKeys) setInject(fn func(code uint16, down bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inject = fn
}
