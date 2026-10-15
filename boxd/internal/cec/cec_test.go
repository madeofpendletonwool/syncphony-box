package cec

import (
	"bytes"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// --- monitor parsing -----------------------------------------------------------

func TestMonitorParserParsesRemoteKeys(t *testing.T) {
	p := &monitorParser{}
	var got []monitorEvent
	for _, line := range []string{
		"Received from TV (0 to 8): USER_CONTROL_PRESSED (0x44):",
		"\tui-cmd: select (0x00)",
		"Received from TV (0 to 8): USER_RELEASED (0x45):",
		"\tui-cmd: select (0x00)",
		"Received from TV (0 to 8): USER_CONTROL_PRESSED (0x44):",
		"\tui-cmd: right (0x04)",
	} {
		got = append(got, p.line(line)...)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(got), got)
	}
	if got[0].kind != evKeyPress || got[0].ui != uiSelect {
		t.Fatalf("event 0: %+v", got[0])
	}
	if got[1].kind != evKeyRelease || got[1].ui != uiSelect {
		t.Fatalf("event 1: %+v", got[1])
	}
	if got[2].kind != evKeyPress || got[2].ui != uiRight {
		t.Fatalf("event 2: %+v", got[2])
	}
}

func TestMonitorParserParsesActiveSource(t *testing.T) {
	p := &monitorParser{}
	var got []monitorEvent
	for _, line := range []string{
		"Received from TV (0 to 15): ACTIVE_SOURCE (0x82):",
		"\tphys-addr: 2.0.0.0",
		"Transmitted by Playback 1 (8 to 15): ACTIVE_SOURCE (0x82):",
		"\tphys-addr: 1.0.0.0",
	} {
		got = append(got, p.line(line)...)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if !got[0].received || got[0].pa != "2.0.0.0" {
		t.Fatalf("received event: %+v", got[0])
	}
	if got[1].received || got[1].pa != "1.0.0.0" {
		t.Fatalf("transmitted event: %+v", got[1])
	}
}

func TestMonitorParserSkipsOtherTraffic(t *testing.T) {
	p := &monitorParser{}
	for _, line := range []string{
		"Received from TV (0 to 8): REPORT_POWER_STATUS (0x90):",
		"\tpower-status: on",
		"Received from TV (0 to 15): SET_STREAM_PATH (0x82):",
		"",
		"Poll: TV (0 to 8)",
	} {
		if evs := p.line(line); len(evs) != 0 {
			t.Fatalf("line %q produced %+v", line, evs)
		}
	}
}

func TestParsePhysAddr(t *testing.T) {
	info := "Driver Name                : vc4\n" +
		"\tPhysical Address           : 1.0.0.0\n" +
		"\tLogical Address Mask       : 0x0100\n"
	if got := parsePhysAddr(info); got != "1.0.0.0" {
		t.Fatalf("got %q", got)
	}
	if got := parsePhysAddr("nothing here"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// --- key mapping ---------------------------------------------------------------

func TestMapKey(t *testing.T) {
	for _, tc := range []struct {
		ui   byte
		want uint16
	}{
		{uiSelect, keyEnter},
		{uiUp, keyUp},
		{uiDown, keyDown},
		{uiLeft, keyLeft},
		{uiRight, keyRight},
		{uiExit, keyEsc},
		{uiPlay, keyPlayPause},
		{uiPause, keyPlayPause},
	} {
		got, ok := mapKey(tc.ui)
		if !ok || got != tc.want {
			t.Fatalf("ui 0x%02x: got %d,%v want %d", tc.ui, got, ok, tc.want)
		}
	}
	for _, ui := range []byte{0x09, 0x45, 0x30, 0xff} { // menus, stop, digits: not ours
		if _, ok := mapKey(ui); ok {
			t.Fatalf("ui 0x%02x should be unmapped", ui)
		}
	}
}

// --- the remote-keys state machine ----------------------------------------------

// recordingKeys builds a remoteKeys whose injections are recorded as
// "code:down|up" strings.
func recordingKeys() (*remoteKeys, func() []string) {
	var mu sync.Mutex
	var log []string
	r := newRemoteKeys(nil)
	r.setInject(func(code uint16, down bool) {
		mu.Lock()
		defer mu.Unlock()
		s := "up"
		if down {
			s = "down"
		}
		log = append(log, itoa(int(code))+":"+s)
	})
	return r, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), log...)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestShortOKClicksOnRelease(t *testing.T) {
	old := longOKDelay
	longOKDelay = 5 * time.Second
	defer func() { longOKDelay = old }()
	r, log := recordingKeys()
	r.press(uiSelect)
	if got := log(); len(got) != 0 {
		t.Fatalf("Enter must wait for the release, got %v", got)
	}
	r.release(uiSelect)
	if got := strings.Join(log(), " "); got != "28:down 28:up" {
		t.Fatalf("want one Enter click, got %q", got)
	}
}

func TestLongOKFiresGestureAndSwallowsTheClick(t *testing.T) {
	old := longOKDelay
	longOKDelay = 20 * time.Millisecond
	defer func() { longOKDelay = old }()

	var fired sync.Mutex
	n := 0
	r := newRemoteKeys(func() { fired.Lock(); n++; fired.Unlock() })
	r.setInject(func(code uint16, down bool) {})
	r.press(uiSelect)
	waitFor(t, func() bool {
		fired.Lock()
		defer fired.Unlock()
		return n == 1
	})
	r.release(uiSelect) // the late release must not click
	fired.Lock()
	defer fired.Unlock()
	if n != 1 {
		t.Fatalf("gesture fired %d times", n)
	}
}

func TestHeldKeyIsReleasedByTheGuard(t *testing.T) {
	old := keyReleaseGuard
	keyReleaseGuard = 20 * time.Millisecond
	defer func() { keyReleaseGuard = old }()
	r, log := recordingKeys()
	r.press(uiRight)
	waitFor(t, func() bool {
		return strings.Join(log(), " ") == "106:down 106:up"
	})
	r.release(uiRight) // a late release is a no-op, not a second up
	if got := log(); len(got) != 2 {
		t.Fatalf("late release injected again: %v", got)
	}
}

func TestSelectRepeatsAreIgnoredWhileHeld(t *testing.T) {
	old := longOKDelay
	longOKDelay = 40 * time.Millisecond
	defer func() { longOKDelay = old }()
	r, log := recordingKeys()
	r.press(uiSelect)
	r.press(uiSelect) // the TV repeats Pressed while held
	r.press(uiSelect)
	if got := log(); len(got) != 0 {
		t.Fatalf("repeats must not click: %v", got)
	}
	r.release(uiSelect)
	if got := strings.Join(log(), " "); got != "28:down 28:up" {
		t.Fatalf("want one click after repeats, got %q", got)
	}
}

// --- the controller: wake, standby, takeover ------------------------------------

// testController builds a controller with every hardware edge stubbed.
type ctlCalls struct {
	mu    sync.Mutex
	calls []string
}

func (c *ctlCalls) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, s)
}

func (c *ctlCalls) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func testController(t *testing.T, cfg Config) (*Controller, *ctlCalls) {
	t.Helper()
	calls := &ctlCalls{}
	c := New(cfg, log.New(io.Discard, "", 0), nil)
	c.cecCtl = func(dev string, args ...string) (string, error) {
		calls.add(dev + " " + strings.Join(args, " "))
		pa := "1.0.0.0"
		if strings.HasSuffix(dev, "1") {
			pa = "2.0.0.0"
		}
		return "\tPhysical Address           : " + pa + "\n", nil
	}
	c.startMonitor = func(dev string) (io.ReadCloser, func(), error) {
		return io.NopCloser(&bytes.Buffer{}), func() {}, nil
	}
	c.openKeydev = func() (keyDevice, error) { return nopKeydev{}, nil }
	return c, calls
}

type nopKeydev struct{}

func (nopKeydev) Press(uint16) error   { return nil }
func (nopKeydev) Release(uint16) error { return nil }
func (nopKeydev) Close() error         { return nil }

func TestWakeAndSwitchSendsBothAndRatesLimits(t *testing.T) {
	c, calls := testController(t, Config{Enabled: true})
	c.mu.Lock()
	c.adapters = []*adapter{{dev: "/dev/cec0", pa: "1.0.0.0"}}
	c.mu.Unlock()

	c.WakeAndSwitch()
	got := strings.Join(calls.list(), ";")
	for _, want := range []string{
		"/dev/cec0 --to=0 --image-view-on",
		"/dev/cec0 --to=0 --active-source=phys-addr=1.0.0.0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	c.mu.Lock()
	taken := c.takenOver
	c.mu.Unlock()
	if !taken {
		t.Fatal("wake should mark the TV as taken over by the box")
	}

	// Within the window: nothing more is sent, however often playback starts.
	before := len(calls.list())
	c.WakeAndSwitch()
	if n := len(calls.list()); n != before {
		t.Fatalf("rate limit failed: %d extra calls", n-before)
	}

	// Past the window it fires again.
	c.mu.Lock()
	c.lastWake = time.Now().Add(-wakeEvery - time.Second)
	c.mu.Unlock()
	c.WakeAndSwitch()
	if n := len(calls.list()); n == before {
		t.Fatal("should wake again after the window")
	}
}

func TestWakePicksTheSecondHDMIAdapter(t *testing.T) {
	c, calls := testController(t, Config{Enabled: true, SecondHDMI: true})
	c.mu.Lock()
	c.adapters = []*adapter{
		{dev: "/dev/cec0", pa: "1.0.0.0"},
		{dev: "/dev/cec1", pa: "2.0.0.0"},
	}
	c.mu.Unlock()
	c.WakeAndSwitch()
	got := strings.Join(calls.list(), ";")
	if !strings.Contains(got, "/dev/cec1 --to=0 --active-source=phys-addr=2.0.0.0") {
		t.Fatalf("hdmi2 should drive cec1: %q", got)
	}
}

func TestStandbyOnlyWhenTakenOverAndIdleEnough(t *testing.T) {
	c, calls := testController(t, Config{Enabled: true, Standby: true, StandbyAfter: 30 * time.Minute})
	c.mu.Lock()
	c.adapters = []*adapter{{dev: "/dev/cec0", pa: "1.0.0.0"}}
	c.mu.Unlock()

	// Not idle long enough, and not taken over: nothing.
	c.Housekeeping(10 * time.Minute)
	c.mu.Lock()
	c.takenOver = true
	c.mu.Unlock()
	c.Housekeeping(10 * time.Minute)
	if got := calls.list(); len(got) != 0 {
		t.Fatalf("standby fired early: %v", got)
	}

	// Idle long enough with the TV ours: one standby, and the flag clears.
	c.Housekeeping(31 * time.Minute)
	got := calls.list()
	if len(got) != 1 || !strings.Contains(got[0], "--standby") {
		t.Fatalf("want one standby, got %v", got)
	}
	c.mu.Lock()
	taken := c.takenOver
	c.mu.Unlock()
	if taken {
		t.Fatal("standby should clear the takeover flag")
	}
	c.Housekeeping(time.Hour) // nothing more: the TV isn't ours anymore
	if n := len(calls.list()); n != 1 {
		t.Fatalf("standby repeated: %v", calls.list())
	}
}

func TestStandbyOffByDefault(t *testing.T) {
	c, calls := testController(t, Config{Enabled: true}) // Standby: false
	c.mu.Lock()
	c.adapters = []*adapter{{dev: "/dev/cec0", pa: "1.0.0.0"}}
	c.takenOver = true
	c.mu.Unlock()
	c.Housekeeping(time.Hour)
	if got := calls.list(); len(got) != 0 {
		t.Fatalf("standby must be opt-in: %v", got)
	}
}

func TestForeignActiveSourceClearsTakeover(t *testing.T) {
	c, _ := testController(t, Config{Enabled: true, Standby: true, StandbyAfter: time.Millisecond})
	c.mu.Lock()
	c.adapters = []*adapter{{dev: "/dev/cec0", pa: "1.0.0.0"}}
	c.takenOver = true
	c.mu.Unlock()

	// The monitor sees the game console become active (a foreign PA).
	traffic := "Received from Tuner 1 (4 to 15): ACTIVE_SOURCE (0x82):\n" +
		"\tphys-addr: 2.0.0.0\n" +
		"Received from TV (0 to 15): ACTIVE_SOURCE (0x82):\n" +
		"\tphys-addr: 1.0.0.0\n" // a TV re-announcing us: not a takeover
	c.mu.Lock()
	a := c.adapters[0]
	a.rc = io.NopCloser(strings.NewReader(traffic))
	c.mu.Unlock()
	c.readMonitor(a)

	c.mu.Lock()
	taken := c.takenOver
	c.mu.Unlock()
	if taken {
		t.Fatal("a foreign Active Source must clear the takeover flag")
	}
}

func TestDisabledControllerDoesNothing(t *testing.T) {
	c, calls := testController(t, Config{Enabled: false})
	c.mu.Lock()
	c.adapters = []*adapter{{dev: "/dev/cec0", pa: "1.0.0.0"}}
	c.mu.Unlock()
	c.WakeAndSwitch()
	c.Housekeeping(time.Hour)
	if got := calls.list(); len(got) != 0 {
		t.Fatalf("cec=off must be inert, got %v", got)
	}
}

// --- the uinput ABI assumption ----------------------------------------------------

func TestUinputStructSizes(t *testing.T) {
	if got := unsafeSize(inputEvent{}); got != 24 {
		t.Fatalf("input_event is %d bytes, want 24", got)
	}
	if got := unsafeSize(uinputUserDev{}); got != 1116 {
		t.Fatalf("uinput_user_dev is %d bytes, want 1116", got)
	}
}

func unsafeSize[T any](v T) int { return int(unsafe.Sizeof(v)) }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
