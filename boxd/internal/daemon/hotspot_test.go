package daemon

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/wifi"
)

// fakeNet plays NetworkManager for the hotspot tests.
type fakeNet struct {
	mu         sync.Mutex
	online     bool
	saved      bool
	nets       []wifi.Network
	connectErr error
	apUp       bool
	calls      []string
	// onConnect runs inside Connect (a join can bring the box online).
	onConnect func()
}

func (f *fakeNet) log(s string) {
	f.calls = append(f.calls, s)
}

func (f *fakeNet) Device() (string, error) { return "wlan0", nil }
func (f *fakeNet) Online() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online, nil
}
func (f *fakeNet) SavedWiFi() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saved
}
func (f *fakeNet) APSSID(string) string { return "Syncphony-Box-ABCD" }
func (f *fakeNet) Scan(string) ([]wifi.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("scan")
	return f.nets, nil
}
func (f *fakeNet) StartAP(dev, ssid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("start " + ssid)
	f.apUp = true
	return nil
}
func (f *fakeNet) StopAP() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("stop")
	f.apUp = false
	return nil
}
func (f *fakeNet) Connect(dev, ssid, password string, hidden bool) error {
	f.mu.Lock()
	f.log("connect " + ssid + " " + password)
	err, on := f.connectErr, f.onConnect
	f.mu.Unlock()
	if err == nil && on != nil {
		on()
	}
	return err
}
func (f *fakeNet) snapshot() (calls []string, apUp bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), f.apUp
}

func newHotspotDaemon(t *testing.T, txt string) (*Daemon, *recorder, *fakeNet) {
	t.Helper()
	d, rec := newTestDaemon(t, txt)
	f := &fakeNet{nets: []wifi.Network{
		{SSID: "Home", Signal: 80, Secure: true},
		{SSID: "Cafe", Signal: 40},
	}}
	d.wifi = f
	d.net.device = "wlan0"
	joinDelay, recheckWait, recheckPoll, healthzRetry = 0, 50*time.Millisecond, 5*time.Millisecond, 0
	t.Cleanup(func() {
		joinDelay, recheckWait, recheckPoll, healthzRetry = time.Second, 45*time.Second, 3*time.Second, 5*time.Second
	})
	return d, rec, f
}

// --- the policy -----------------------------------------------------------------

func TestDecideNet(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		v    netView
		want netAction
	}{
		{"online: nothing", netView{now: t0, online: true}, netNothing},
		{"fresh box, 59 s offline", netView{now: t0, offlineSince: t0.Add(-59 * time.Second)}, netNothing},
		{"fresh box, 60 s offline: hotspot", netView{now: t0, offlineSince: t0.Add(-60 * time.Second)}, netStartAP},
		{"seen online, 60 s offline: still the offline screen",
			netView{now: t0, seenOnline: true, offlineSince: t0.Add(-60 * time.Second)}, netNothing},
		{"seen online, 4m59s offline", netView{now: t0, seenOnline: true, offlineSince: t0.Add(-299 * time.Second)}, netNothing},
		{"seen online, 5 min offline: hotspot",
			netView{now: t0, seenOnline: true, offlineSince: t0.Add(-5 * time.Minute)}, netStartAP},
		{"hotspot up, Ethernet comes up: stop",
			netView{now: t0, phase: phaseHotspot, online: true}, netStopAP},
		{"hotspot up, nothing saved: stay",
			netView{now: t0, phase: phaseHotspot, apSince: t0.Add(-time.Hour)}, netNothing},
		{"hotspot up 5 min, a saved network, portal quiet: recheck",
			netView{now: t0, phase: phaseHotspot, savedWiFi: true, apSince: t0.Add(-5 * time.Minute)}, netRecheck},
		{"hotspot up, a saved network, but a phone is on the portal",
			netView{now: t0, phase: phaseHotspot, savedWiFi: true, apSince: t0.Add(-time.Hour),
				lastPortal: t0.Add(-time.Minute)}, netNothing},
		{"hotspot up 4 min, a saved network: not yet",
			netView{now: t0, phase: phaseHotspot, savedWiFi: true, apSince: t0.Add(-4 * time.Minute)}, netNothing},
		{"joining: hands off",
			netView{now: t0, joining: true, offlineSince: t0.Add(-time.Hour)}, netNothing},
		{"checking: hands off",
			netView{now: t0, phase: phaseChecking, offlineSince: t0.Add(-time.Hour)}, netNothing},
	} {
		if got, _ := decideNet(tc.v); got != tc.want {
			t.Errorf("%s: decideNet = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// --- the watch loop ----------------------------------------------------------------

func TestNetTickStartsHotspotAfterBootGrace(t *testing.T) {
	d, _, f := newHotspotDaemon(t, "")
	t0 := time.Now()
	d.netTick(t0)
	d.netTick(t0.Add(30 * time.Second))
	if _, up := f.snapshot(); up {
		t.Fatal("hotspot up before the boot grace")
	}
	d.netTick(t0.Add(bootGrace))
	calls, up := f.snapshot()
	if !up || !d.hotspotUp() {
		t.Fatalf("hotspot not up after the boot grace: %q", calls)
	}
	if calls[0] != "scan" {
		t.Errorf("calls = %q, want a scan before the AP takes the radio", calls)
	}
	if !d.wifiSetup() {
		t.Error("the TV should belong to the hotspot screen")
	}

	// Ethernet plugged in: the hotspot goes away.
	f.mu.Lock()
	f.online = true
	f.mu.Unlock()
	d.netTick(t0.Add(bootGrace + 5*time.Second))
	if _, up := f.snapshot(); up || d.hotspotUp() {
		t.Fatal("hotspot still up with a connection")
	}
}

func TestNetTickRemembersBeingOnline(t *testing.T) {
	d, _, f := newHotspotDaemon(t, "")
	f.online = true
	d.netTick(time.Now())
	if _, err := os.Stat(seenMarker()); err != nil {
		t.Fatalf("marker: %v", err)
	}
	// A restarted boxd (a reboot) reads it back: the 5-minute grace.
	dir := boxdDir()
	d2, _ := newTestDaemon(t, "")
	if d2.net.seenOnline {
		t.Fatal("a fresh /data claims the box has been online")
	}
	t.Setenv("SB_BOXD_DIR", dir)
	if d3 := New("test-version", d2.log, d2.run); !d3.net.seenOnline {
		t.Fatal("seenOnline not restored from /data")
	}
}

func TestRecheckFindsSavedNetwork(t *testing.T) {
	d, _, f := newHotspotDaemon(t, "")
	d.netTick(time.Now())
	d.netTick(time.Now().Add(bootGrace))
	if !d.hotspotUp() {
		t.Fatal("no hotspot")
	}
	f.mu.Lock()
	f.saved = true
	f.online = true // the router came back
	f.mu.Unlock()
	d.recheck()
	if d.wifiSetup() {
		t.Fatal("still in setup after the saved network came back")
	}
	if _, up := f.snapshot(); up {
		t.Fatal("AP still up")
	}
}

func TestRecheckBringsHotspotBack(t *testing.T) {
	d, _, f := newHotspotDaemon(t, "")
	d.netTick(time.Now())
	d.netTick(time.Now().Add(bootGrace))
	d.recheck()
	calls, up := f.snapshot()
	if !up || !d.hotspotUp() {
		t.Fatalf("hotspot not back: %q", calls)
	}
}

// --- the captive portal -------------------------------------------------------------

func lanGet(t *testing.T, d *Daemon, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	d.lanMux().ServeHTTP(rec, req)
	return rec
}

func lanPost(t *testing.T, d *Daemon, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Host = wifi.APAddress
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	d.lanMux().ServeHTTP(rec, req)
	return rec
}

func upHotspot(t *testing.T, d *Daemon) {
	t.Helper()
	d.netOp.Lock()
	d.startHotspot("test")
	d.netOp.Unlock()
}

func TestPortalRedirectsProbes(t *testing.T) {
	d, _, _ := newHotspotDaemon(t, "server_url=https://s.example\n")
	upHotspot(t, d)
	for _, probe := range []struct{ host, path string }{
		{"connectivitycheck.gstatic.com", "/generate_204"},
		{"captive.apple.com", "/hotspot-detect.html"},
		{"www.msftconnecttest.com", "/connecttest.txt"},
	} {
		rec := lanGet(t, d, probe.host, probe.path)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "http://10.42.0.1/" {
			t.Errorf("%s%s: %d → %q, want a redirect to the portal", probe.host, probe.path,
				rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestPortalFormListsNetworks(t *testing.T) {
	d, _, _ := newHotspotDaemon(t, "")
	upHotspot(t, d)
	rec := lanGet(t, d, wifi.APAddress, "/")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{`value="Home"`, `value="Cafe"`, `name="password"`, `name="server_url"`, "Syncphony-Box-ABCD"} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q", want)
		}
	}

	// A configured box: the form is only about Wi-Fi.
	d2, _, _ := newHotspotDaemon(t, "server_url=https://s.example\n")
	upHotspot(t, d2)
	if body := lanGet(t, d2, wifi.APAddress, "/").Body.String(); strings.Contains(body, `name="server_url"`) {
		t.Error("configured box still asks for the server")
	}
	// And the normal LAN form is not what the portal serves.
	if body := lanGet(t, d2, wifi.APAddress, "/").Body.String(); strings.Contains(body, "already set up") {
		t.Error("portal served the closed page")
	}
}

func TestPortalRejectsBadInput(t *testing.T) {
	d, _, f := newHotspotDaemon(t, "")
	upHotspot(t, d)
	for _, tc := range []struct {
		name string
		form url.Values
		want string
	}{
		{"no network", url.Values{}, "Pick your Wi-Fi"},
		{"secure without password", url.Values{"ssid": {"Home"}}, "needs its Wi-Fi password"},
		{"short password", url.Values{"ssid": {"Home"}, "password": {"short"}}, "8 to 63"},
		{"bad server", url.Values{"ssid": {"Cafe"}, "server_url": {"ftp://x"}}, "look like a server address"},
	} {
		rec := lanPost(t, d, "/wifi", tc.form)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d, body missing %q", tc.name, rec.Code, tc.want)
		}
	}
	if calls, _ := f.snapshot(); strings.Contains(strings.Join(calls, ";"), "connect") {
		t.Errorf("a rejected form still joined: %q", calls)
	}
}

func TestPortalJoinWithServer(t *testing.T) {
	d, rec, f := newHotspotDaemon(t, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	f.onConnect = func() {
		f.mu.Lock()
		f.online = true
		f.mu.Unlock()
	}
	upHotspot(t, d)

	resp := lanPost(t, d, "/wifi", url.Values{
		"ssid": {"Home"}, "password": {"hunter2222"}, "server_url": {srv.URL}, "name": {"Den TV"},
	})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "Connecting to Home") {
		t.Fatalf("join: %d %s", resp.Code, resp.Body)
	}
	waitFor(t, func() bool { return !d.wifiSetup() })

	calls, up := f.snapshot()
	if up {
		t.Fatal("AP still up after a good join")
	}
	if !strings.Contains(strings.Join(calls, ";"), "connect Home hunter2222") {
		t.Errorf("calls = %q", calls)
	}
	// The new Wi-Fi went to /data right away, the server URL into
	// syncphony.txt, and the kiosk to the server.
	waitFor(t, func() bool {
		return strings.Contains(strings.Join(rec.calls(), ";"), "systemctl restart syncphony-kiosk.service")
	})
	if !strings.Contains(strings.Join(rec.calls(), ";"), dataPersist+" save") {
		t.Errorf("no data-persist save: %q", rec.calls())
	}
	if c := config.ParseFile(config.PathsFromEnv().Txt); c.ServerURL != srv.URL || c.Name != "Den TV" {
		t.Fatalf("saved config: %+v", c)
	}
	if _, err := os.Stat(seenMarker()); err != nil {
		t.Errorf("the join didn't record the box as having been online: %v", err)
	}
}

func TestPortalJoinUnreachableServerLeavesNotice(t *testing.T) {
	d, _, f := newHotspotDaemon(t, "")
	f.onConnect = func() {
		f.mu.Lock()
		f.online = true
		f.mu.Unlock()
	}
	upHotspot(t, d)
	lanPost(t, d, "/wifi", url.Values{"ssid": {"Cafe"}, "server_url": {"http://127.0.0.1:1"}})
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.net.notice != ""
	})
	if d.configured() {
		t.Fatal("an unreachable server was saved")
	}
	// The TV's setup screen says what's left to do.
	if body := get(t, d.bridgeMux(), "/setup").Body.String(); !strings.Contains(body, "couldn&#39;t reach") {
		t.Errorf("setup screen without the notice:\n%s", body)
	}
}

func TestPortalJoinFailureBringsHotspotBack(t *testing.T) {
	d, _, f := newHotspotDaemon(t, "server_url=https://s.example\n")
	f.connectErr = errors.New("couldn't join Home — is the password right?")
	upHotspot(t, d)

	lanPost(t, d, "/wifi", url.Values{"ssid": {"Home"}, "password": {"wrongpass"}})
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.net.joining == "" && d.net.phase == phaseHotspot
	})
	if _, up := f.snapshot(); !up {
		t.Fatal("AP not back after a failed join")
	}
	// The TV says why; so does the form when the phone comes back.
	tv := get(t, d.bridgeMux(), "/hotspot").Body.String()
	if !strings.Contains(tv, "is the password right?") {
		t.Errorf("TV screen without the error:\n%s", tv)
	}
	if form := lanGet(t, d, wifi.APAddress, "/").Body.String(); !strings.Contains(form, "is the password right?") {
		t.Error("form without the error")
	}
}

// --- the TV screens ---------------------------------------------------------------

func TestScreensFollowTheHotspot(t *testing.T) {
	d, _, _ := newHotspotDaemon(t, "server_url=https://s.example\n")
	if rec := get(t, d.bridgeMux(), "/hotspot"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/start" {
		t.Fatalf("/hotspot without a hotspot: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	upHotspot(t, d)
	for _, path := range []string{"/start", "/setup"} {
		rec := get(t, d.bridgeMux(), path)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/hotspot" {
			t.Errorf("%s with the hotspot up: %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := get(t, d.bridgeMux(), "/hotspot")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Syncphony-Box-ABCD") ||
		!strings.Contains(body, "data:image/png;base64,") || !strings.Contains(body, "http://10.42.0.1") {
		t.Fatalf("hotspot screen: %d\n%s", rec.Code, body)
	}

	st := get(t, d.bridgeMux(), "/status").Body.String()
	if !strings.Contains(st, `"wifiSetup":true`) || !strings.Contains(st, `"hotspot":"Syncphony-Box-ABCD"`) {
		t.Errorf("/status = %s", st)
	}
}
