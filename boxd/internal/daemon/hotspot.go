package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/wifi"
)

// The Wi-Fi setup hotspot (MAD-808, ADR 0010): setup with no network at
// all. A box with neither Ethernet nor Wi-Fi becomes its own open Wi-Fi
// network, Syncphony-Box-XXXX, and boxd's LAN listener becomes that
// network's captive portal — a phone joins (the TV shows the join QR),
// the portal opens, and the form takes the house Wi-Fi (and the server
// URL, if the box has none yet). Saving gives the radio back and joins;
// on failure the hotspot comes back and the TV says why.
const (
	// bootGrace: a box that has never been online starts the hotspot
	// this long after boxd starts (≈ boot) without a connection — long
	// enough for Ethernet DHCP and an Imager-configured Wi-Fi to come up.
	bootGrace = 60 * time.Second

	// lostGrace: a box that has been online before (a marker on /data)
	// gets this long — at boot or mid-session — before it gives up its
	// Wi-Fi for the hotspot. Until then the TV shows the offline screen:
	// a router reboot must never put the box into setup mode.
	lostGrace = 5 * time.Minute

	// recheckEvery: while the hotspot is up and a Wi-Fi network is saved,
	// give the radio back this often to look for it (the router may have
	// come back) — but only when nobody has used the portal for
	// portalQuiet, so a phone mid-form is never dropped.
	recheckEvery = 5 * time.Minute
	portalQuiet  = 2 * time.Minute

	// tvQuiet: someone is choosing a network on the TV's own Wi-Fi screen
	// (it pings /wifi/hold while in use). The hotspot doesn't start under
	// them; it needs the radio the screen is scanning with.
	tvQuiet = 30 * time.Second

	dataPersist = "/usr/lib/syncphony-box/data-persist"
)

// Waits in the join and recheck flows; vars so tests run them instantly.
var (
	joinDelay     = 1 * time.Second  // let the phone get its answer before the AP goes away
	recheckWait   = 45 * time.Second // how long NetworkManager gets to find a saved network
	recheckPoll   = 3 * time.Second
	healthzTries  = 24 // two minutes after a join for DHCP, DNS and the clock (see joinWiFi)
	healthzRetry  = 5 * time.Second
	netWatchEvery = 5 * time.Second
)

// netDriver is what boxd needs from NetworkManager (wifi.NM); an
// interface so the daemon's tests can play the network.
type netDriver interface {
	Device() (string, error)
	Online() (bool, error)
	SavedWiFi() bool
	APSSID(dev string) string
	Scan(dev string) ([]wifi.Network, error)
	StartAP(dev, ssid string) error
	StopAP() error
	Connect(dev, ssid, password string, hidden bool) error
}

type netPhase string

const (
	phaseNone     netPhase = ""
	phaseHotspot  netPhase = "hotspot"  // the setup AP is up
	phaseChecking netPhase = "checking" // AP down for a moment, looking for a saved network
)

// netState is the hotspot's slice of the daemon state (under d.mu).
type netState struct {
	device       string // the Wi-Fi interface; empty = no hotspot possible
	phase        netPhase
	apSSID       string
	apSince      time.Time
	networks     []wifi.Network // the last scan, taken before the AP came up
	joining      string         // a phone asked to join this network; in progress
	lastError    string         // why the last join failed (TV and form)
	notice       string         // after a join: what still needs doing (setup screen)
	offlineSince time.Time      // no connection continuously since (zero = online)
	seenOnline   bool           // this box has been online before (persisted)
	lastPortal   time.Time      // last request to the captive portal
	lastTV       time.Time      // last use of the TV's own Wi-Fi screen
	deviceErr    string         // why there is no Wi-Fi device (yet)
	startErr     string         // why the hotspot couldn't start, last time it tried
}

// netView is the locked snapshot decideNet runs on.
type netView struct {
	now          time.Time
	online       bool
	phase        netPhase
	joining      bool
	seenOnline   bool
	offlineSince time.Time
	apSince      time.Time
	lastPortal   time.Time
	lastTV       time.Time
	savedWiFi    bool
}

type netAction string

const (
	netNothing netAction = ""
	netStartAP netAction = "start"
	netStopAP  netAction = "stop"
	netRecheck netAction = "recheck"
)

// decideNet is the whole hotspot policy, pure on its input (the const
// block above has the reasoning).
func decideNet(v netView) (netAction, string) {
	if v.joining || v.phase == phaseChecking {
		return netNothing, "" // a join or a recheck owns the radio
	}
	if v.phase == phaseHotspot {
		if v.online {
			// The AP holds the Wi-Fi radio, so this is Ethernet: setup
			// is moot, give the radio back.
			return netStopAP, "a connection came up"
		}
		if v.savedWiFi && v.now.Sub(v.apSince) >= recheckEvery && v.now.Sub(v.lastPortal) >= portalQuiet {
			return netRecheck, "looking for a saved network"
		}
		return netNothing, ""
	}
	if v.online || v.offlineSince.IsZero() || v.now.Sub(v.lastTV) < tvQuiet {
		return netNothing, ""
	}
	grace := hotspotGrace(v.seenOnline)
	if v.now.Sub(v.offlineSince) >= grace {
		return netStartAP, fmt.Sprintf("no Ethernet or Wi-Fi connection for %s", grace)
	}
	return netNothing, ""
}

// hotspotGrace is how long a box without a connection waits before it
// starts the hotspot.
func hotspotGrace(seenOnline bool) time.Duration {
	if seenOnline {
		return lostGrace
	}
	return bootGrace
}

// boxdDir is boxd's own state directory on /data (docs/persistent-paths.md);
// SB_BOXD_DIR moves it in dev and tests.
func boxdDir() string {
	if v := os.Getenv("SB_BOXD_DIR"); v != "" {
		return v
	}
	return filepath.Join(config.DefaultDataDir, "boxd")
}

// seenMarker records that this box has been online once: from then on a
// lost network gets the offline screen and lostGrace, not setup after a
// minute.
func seenMarker() string { return filepath.Join(boxdDir(), "network-seen") }

func (d *Daemon) markSeenOnline() {
	if err := os.WriteFile(seenMarker(), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		d.log.Printf("boxd: wifi: cannot record the first connection: %v", err)
	}
}

// hotspotUp reports whether the setup AP is up (the LAN listener is the
// captive portal then).
func (d *Daemon) hotspotUp() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.net.phase == phaseHotspot
}

// wifiSetup reports whether the TV belongs to the hotspot screen: the AP
// is up, a join is in flight, or a recheck has the radio for a moment.
func (d *Daemon) wifiSetup() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.net.phase != phaseNone || d.net.joining != ""
}

// netWatch is the hotspot's background loop. It waits for a Wi-Fi device
// (NetworkManager and the driver can come up after boxd); a box with
// none, or no NetworkManager at all (a dev machine), logs once and the
// hotspot stays inert.
func (d *Daemon) netWatch() {
	tick := time.NewTicker(netWatchEvery)
	defer tick.Stop()
	logged := false
	for {
		dev, err := d.wifi.Device()
		d.mu.Lock()
		if err == nil {
			d.net.device, d.net.deviceErr = dev, ""
		} else {
			d.net.deviceErr = err.Error()
		}
		d.mu.Unlock()
		if err == nil {
			break
		}
		if !logged {
			d.log.Printf("boxd: wifi: setup hotspot unavailable for now: %v", err)
			logged = true
		}
		<-tick.C
	}
	for {
		d.netTick(time.Now())
		<-tick.C
	}
}

// netTick samples the network, decides, acts. The NetworkManager work
// is serialized by netOp; a tick that finds a join or a recheck running
// skips (the next one comes 5 s later).
func (d *Daemon) netTick(now time.Time) {
	if !d.netOp.TryLock() {
		return
	}
	defer d.netOp.Unlock()
	online, err := d.wifi.Online()
	if err != nil {
		return
	}
	d.mu.Lock()
	first := false
	if online {
		d.net.offlineSince = time.Time{}
		if !d.net.seenOnline {
			d.net.seenOnline, first = true, true
		}
	} else if d.net.offlineSince.IsZero() {
		d.net.offlineSince = now
	}
	v := netView{
		now:          now,
		online:       online,
		phase:        d.net.phase,
		joining:      d.net.joining != "",
		seenOnline:   d.net.seenOnline,
		offlineSince: d.net.offlineSince,
		apSince:      d.net.apSince,
		lastPortal:   d.net.lastPortal,
		lastTV:       d.net.lastTV,
	}
	d.mu.Unlock()
	if first {
		d.markSeenOnline()
	}
	if v.phase == phaseHotspot {
		v.savedWiFi = d.wifi.SavedWiFi()
	}
	switch act, reason := decideNet(v); act {
	case netStartAP:
		d.startHotspot(reason)
	case netStopAP:
		d.stopHotspot(reason)
	case netRecheck:
		d.recheck()
	}
}

// startHotspot scans (the radio is free now — not once the AP has it)
// and brings the setup AP up. Called with netOp held.
func (d *Daemon) startHotspot(reason string) {
	d.mu.Lock()
	dev := d.net.device
	d.mu.Unlock()
	nets, err := d.wifi.Scan(dev)
	if err != nil {
		d.log.Printf("boxd: wifi: scan: %v", err)
	}
	ssid := d.wifi.APSSID(dev)
	if err := d.wifi.StartAP(dev, ssid); err != nil {
		d.log.Printf("boxd: wifi: cannot start the setup hotspot: %v", err)
		d.mu.Lock()
		d.net.phase = phaseNone
		d.net.startErr = err.Error()    // the TV says why
		d.net.offlineSince = time.Now() // re-arm the grace instead of retrying every tick
		if nets != nil {
			d.net.networks = nets // the TV's own Wi-Fi screen can still use them
		}
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	d.net.phase = phaseHotspot
	d.net.startErr = ""
	d.net.apSSID = ssid
	d.net.apSince = time.Now()
	if nets != nil {
		d.net.networks = nets
	}
	d.mu.Unlock()
	d.log.Printf("boxd: wifi: %s; setup hotspot %s up at %s (%d networks in range)", reason, ssid, wifi.APAddress, len(nets))
}

// stopHotspot takes the AP down. Called with netOp held.
func (d *Daemon) stopHotspot(reason string) {
	if err := d.wifi.StopAP(); err != nil {
		d.log.Printf("boxd: wifi: stop hotspot: %v", err)
	}
	d.mu.Lock()
	d.net.phase = phaseNone
	d.mu.Unlock()
	d.log.Printf("boxd: wifi: %s; setup hotspot down", reason)
}

// recheck gives the radio back for a moment so NetworkManager can find a
// saved network that has come back; if none turns up, the hotspot
// returns (with a fresh scan). Called with netOp held.
func (d *Daemon) recheck() {
	d.mu.Lock()
	d.net.phase = phaseChecking
	d.mu.Unlock()
	d.log.Printf("boxd: wifi: hotspot down for up to %s to look for a saved network", recheckWait)
	d.wifi.StopAP()
	deadline := time.Now().Add(recheckWait)
	for time.Now().Before(deadline) {
		time.Sleep(recheckPoll)
		if ok, _ := d.wifi.Online(); ok {
			d.mu.Lock()
			d.net.phase = phaseNone
			d.net.offlineSince = time.Time{}
			d.mu.Unlock()
			d.log.Printf("boxd: wifi: a saved network is back; setup hotspot stays down")
			return
		}
	}
	d.startHotspot("no saved network in range")
}

// joinWiFi is the save from the portal (or the TV's own Wi-Fi screen),
// run after the asker got its answer: give the radio back, join, and on
// success persist the network and (if the form had it) the server URL.
// On failure from the portal the hotspot comes back and the TV shows
// why; from the TV, its Wi-Fi screen shows why and the hotspot keeps to
// its usual timing.
func (d *Daemon) joinWiFi(ssid, password string, hidden bool, serverURL, name string, fromTV bool) {
	d.netOp.Lock()
	defer d.netOp.Unlock()
	if !fromTV {
		time.Sleep(joinDelay)
	}

	d.mu.Lock()
	dev := d.net.device
	d.net.phase = phaseNone
	d.mu.Unlock()
	d.wifi.StopAP()
	d.log.Printf("boxd: wifi: joining %s", ssid)

	if err := d.wifi.Connect(dev, ssid, password, hidden); err != nil {
		d.log.Printf("boxd: wifi: %v", err)
		d.mu.Lock()
		d.net.joining = ""
		d.net.lastError = capitalize(err.Error())
		if fromTV {
			d.net.lastTV = time.Now() // the screen is still in use: retry, not hotspot
		}
		d.mu.Unlock()
		if !fromTV {
			d.startHotspot("the join failed")
		}
		return
	}

	d.mu.Lock()
	d.net.joining, d.net.lastError = "", ""
	d.net.offlineSince = time.Time{}
	first := !d.net.seenOnline
	d.net.seenOnline = true
	d.mu.Unlock()
	if first {
		d.markSeenOnline()
	}
	d.log.Printf("boxd: wifi: joined %s", ssid)
	// The connection now sits in /etc/NetworkManager — the overlay's
	// tmpfs. Snapshot it to /data now rather than at the next 15-minute
	// save, so a power pull right after setup doesn't forget it.
	if err := d.run(dataPersist, "save"); err != nil {
		d.log.Printf("boxd: wifi: data-persist save: %v", err)
	}

	if serverURL == "" || d.configured() {
		return
	}
	// The clock is usually what's slowest: a box without an RTC boots
	// months behind, and every https certificate looks invalid until NTP
	// corrects it, so the server "can't be reached" and isn't saved.
	// timesyncd has been failing while the box was offline and retries
	// on its own schedule; a restart asks NTP now, on the new network.
	d.run("systemctl", "try-restart", "systemd-timesyncd.service")
	var lastErr error
	for i := 0; i < healthzTries; i++ {
		if i > 0 {
			time.Sleep(healthzRetry)
		}
		if lastErr = healthz(serverURL); lastErr == nil {
			if err := d.saveServer(serverURL, name, "the Wi-Fi setup"); err == nil {
				return
			}
			break
		}
	}
	if lastErr != nil {
		d.log.Printf("boxd: wifi: joined %s, but %s didn't answer: %v", ssid, serverURL, lastErr)
	}
	d.mu.Lock()
	d.net.notice = fmt.Sprintf("Joined %s, but couldn't reach %s. Set the server from a phone on %s.", ssid, serverURL, ssid)
	d.mu.Unlock()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// servePortal is the LAN listener while the hotspot is up: the captive
// portal. Every name resolves to the box (the dnsmasq catch-all), so a
// phone's connectivity probe lands here; anything that isn't the form
// at the box's own address is redirected to it, which is what makes the
// phone pop the portal open.
func (d *Daemon) servePortal(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.net.lastPortal = time.Now()
	d.mu.Unlock()
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != wifi.APAddress {
		http.Redirect(w, r, "http://"+wifi.APAddress+"/", http.StatusFound)
		return
	}
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		writePage(w, wifiFormHTML(d.wifiForm("")))
	case r.URL.Path == "/wifi" && r.Method == http.MethodPost:
		d.handleWiFiSave(w, r)
	default:
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

func (d *Daemon) wifiForm(errMsg string) wifiFormData {
	d.mu.Lock()
	data := wifiFormData{
		APSSID:    d.net.apSSID,
		Networks:  append([]wifi.Network(nil), d.net.networks...),
		AskServer: d.cfg.ServerURL == "",
		Error:     errMsg,
	}
	if data.Error == "" {
		data.Error = d.net.lastError
	}
	d.mu.Unlock()
	data.Name = d.displayName()
	return data
}

func (d *Daemon) handleWiFiSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		writePage(w, wifiFormHTML(d.wifiForm(msg)))
	}
	ssid, password := joinRequest(r)
	serverURL := strings.TrimSpace(r.PostFormValue("server_url"))
	name := strings.TrimSpace(r.PostFormValue("name"))

	known, problem := d.checkJoin(ssid, password)
	if problem != "" {
		fail(problem)
		return
	}
	d.mu.Lock()
	askServer := d.cfg.ServerURL == ""
	d.mu.Unlock()
	if !askServer {
		serverURL, name = "", ""
	}
	if serverURL != "" {
		u, err := url.Parse(serverURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fail("That doesn't look like a server address — it should be like https://syncphony.example.com")
			return
		}
	}

	d.mu.Lock()
	if busy := d.net.joining; busy != "" {
		d.mu.Unlock()
		fail("Already joining " + busy + " — watch the TV.")
		return
	}
	d.net.joining = ssid
	d.net.lastError, d.net.notice = "", ""
	apSSID := d.net.apSSID
	d.mu.Unlock()

	d.log.Printf("boxd: wifi: join %s requested from %s", ssid, r.RemoteAddr)
	writePage(w, joiningPageHTML(ssid, apSSID))
	go d.joinWiFi(ssid, password, !known, serverURL, name, false)
}

// joinRequest reads the network and password from a join form: a typed
// name (a network that isn't listed) wins over the picked one.
func joinRequest(r *http.Request) (ssid, password string) {
	ssid = strings.TrimSpace(r.PostFormValue("other_ssid"))
	if ssid == "" {
		ssid = r.PostFormValue("ssid")
	}
	return ssid, r.PostFormValue("password") // spaces are legal in a passphrase
}

// checkJoin vets a join against the last scan before anything changes,
// so the asker sees the mistake while still connected. known says the
// network was in the scan (one that wasn't is joined as hidden); problem
// is what to tell them, empty when the join can go ahead.
func (d *Daemon) checkJoin(ssid, password string) (known bool, problem string) {
	if ssid == "" {
		return false, "Pick your Wi-Fi network, or type its name."
	}
	d.mu.Lock()
	secure := false
	for _, nw := range d.net.networks {
		if nw.SSID == ssid {
			known, secure = true, nw.Secure
		}
	}
	d.mu.Unlock()
	if secure && password == "" {
		return known, ssid + " needs its Wi-Fi password."
	}
	if password != "" && (len(password) < 8 || len(password) > 64) {
		return known, "Wi-Fi passwords are 8 to 63 characters — check it and try again."
	}
	return known, ""
}

// handleHotspotScreen is the TV side: join the box's network (QR), then
// a page opens on the phone. It follows the join live and goes back to
// the start page once the hotspot is done.
func (d *Daemon) handleHotspotScreen(w http.ResponseWriter, r *http.Request) {
	if !d.wifiSetup() {
		http.Redirect(w, r, "/start", http.StatusFound)
		return
	}
	d.mu.Lock()
	data := hotspotScreenData{
		SSID:     d.net.apSSID,
		Phase:    string(d.net.phase),
		Joining:  d.net.joining,
		Error:    d.net.lastError,
		Address:  "http://" + wifi.APAddress,
		Checking: d.net.phase == phaseChecking,
	}
	d.mu.Unlock()
	if data.SSID != "" {
		qr, err := qrPNG(wifi.JoinString(data.SSID))
		if err != nil {
			d.log.Printf("boxd: qr: %v", err)
		}
		data.QR = qr
	}
	writePage(w, hotspotScreenHTML(data))
}

// wifiStatus is the hotspot's part of /status. Beyond the phase: whether
// the box has a Wi-Fi device at all (and why not), why the hotspot last
// failed to start, and — while the box is offline and waiting — how many
// seconds until it starts (hotspotIn), for the setup screen's countdown.
func (d *Daemon) wifiStatus() map[string]any {
	return d.wifiStatusAt(time.Now())
}

func (d *Daemon) wifiStatusAt(now time.Time) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := map[string]any{
		"phase":      string(d.net.phase),
		"hotspot":    d.net.apSSID,
		"joining":    d.net.joining,
		"error":      d.net.lastError,
		"notice":     d.net.notice,
		"device":     d.net.device != "",
		"startError": d.net.startErr,
	}
	if d.net.device == "" {
		st["unavailable"] = d.net.deviceErr
	}
	if d.net.device != "" && d.net.phase == phaseNone && d.net.joining == "" && !d.net.offlineSince.IsZero() {
		left := hotspotGrace(d.net.seenOnline) - now.Sub(d.net.offlineSince)
		st["hotspotIn"] = max(0, int((left+time.Second-1)/time.Second))
	}
	return st
}

// --- the TV's own Wi-Fi controls ------------------------------------------------
//
// For a box with a keyboard or a mouse (a PC, or a Pi with one plugged
// in), or a remote for the buttons: start the hotspot now instead of
// waiting out the grace, or skip the phone and pick the network on the
// TV. Served on loopback only, to the kiosk's own pages (screenOnly).

var errNetBusy = errors.New("the Wi-Fi is busy (a join or a check is running); try again in a moment")

// startHotspotNow is the setup screen's "Start setup Wi-Fi now" button:
// the hotspot without the grace. It returns once the request is vetted;
// the scan and the access point come up in the background, and the
// screen follows through /status.
func (d *Daemon) startHotspotNow() error {
	d.mu.Lock()
	dev, phase, joining := d.net.device, d.net.phase, d.net.joining
	d.mu.Unlock()
	switch {
	case dev == "":
		return errors.New("this box has no Wi-Fi it can use")
	case phase != phaseNone || joining != "":
		return nil // already up, or about to be
	}
	if online, _ := d.wifi.Online(); online {
		return errors.New("this box is already connected")
	}
	d.mu.Lock()
	d.net.startErr = "" // this attempt's outcome, not the last one's
	d.mu.Unlock()
	go func() {
		d.netOp.Lock()
		defer d.netOp.Unlock()
		d.mu.Lock()
		busy := d.net.phase != phaseNone || d.net.joining != ""
		d.mu.Unlock()
		if !busy {
			d.startHotspot("started from the TV")
		}
	}()
	return nil
}

// scanForTV is the TV Wi-Fi screen's list: a fresh scan, which needs the
// radio, so a running hotspot gives it up (whoever is at the TV chose to
// set up here). It holds the hotspot off for tvQuiet from now.
func (d *Daemon) scanForTV() ([]wifi.Network, error) {
	if !d.netOp.TryLock() {
		return nil, errNetBusy
	}
	defer d.netOp.Unlock()
	d.mu.Lock()
	dev, phase := d.net.device, d.net.phase
	d.net.lastTV = time.Now()
	d.mu.Unlock()
	if dev == "" {
		return nil, errors.New("this box has no Wi-Fi it can use")
	}
	if phase == phaseHotspot {
		d.stopHotspot("Wi-Fi is being set up on the TV")
	}
	nets, err := d.wifi.Scan(dev)
	if err != nil {
		d.log.Printf("boxd: wifi: scan from the TV: %v", err)
		return nil, fmt.Errorf("couldn't look for networks: %w", err)
	}
	d.mu.Lock()
	d.net.networks = nets
	d.mu.Unlock()
	return nets, nil
}

func (d *Daemon) handleWiFiScreen(w http.ResponseWriter, r *http.Request) {
	d.holdForTV()
	writePage(w, wifiScreenHTML())
}

func (d *Daemon) holdForTV() {
	d.mu.Lock()
	d.net.lastTV = time.Now()
	d.mu.Unlock()
}

func (d *Daemon) handleWiFiHold(w http.ResponseWriter, r *http.Request) {
	d.holdForTV()
	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleWiFiHotspotNow(w http.ResponseWriter, r *http.Request) {
	if err := d.startHotspotNow(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": capitalize(err.Error()) + "."})
		return
	}
	d.log.Printf("boxd: wifi: setup hotspot requested from the TV")
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (d *Daemon) handleWiFiScan(w http.ResponseWriter, r *http.Request) {
	nets, err := d.scanForTV()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": capitalize(err.Error())})
		return
	}
	list := make([]map[string]any, 0, len(nets))
	for _, nw := range nets {
		list = append(list, map[string]any{"ssid": nw.SSID, "signal": nw.Signal, "secure": nw.Secure})
	}
	writeJSON(w, http.StatusOK, map[string]any{"networks": list})
}

// handleWiFiJoin is the TV Wi-Fi screen's Connect: the same checks as
// the portal's form, then the join in the background (the screen follows
// it through /status).
func (d *Daemon) handleWiFiJoin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Bad request."})
		return
	}
	ssid, password := joinRequest(r)
	known, problem := d.checkJoin(ssid, password)
	if problem != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": problem})
		return
	}
	d.mu.Lock()
	if busy := d.net.joining; busy != "" {
		d.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Already joining " + busy + "."})
		return
	}
	d.net.joining = ssid
	d.net.lastError, d.net.notice = "", ""
	d.net.lastTV = time.Now()
	d.mu.Unlock()
	d.log.Printf("boxd: wifi: join %s requested from the TV", ssid)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	go d.joinWiFi(ssid, password, !known, "", "", true)
}

// screenOnly admits only the kiosk's own pages: addressed as loopback
// (no DNS rebinding) and, when the browser says where the request comes
// from, from boxd's own origin — not the server's page, not anything else.
func screenOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		own := strings.TrimPrefix(origin, "http://")
		if !allowedHost(r.Host) || (origin != "" && (own == origin || !allowedHost(own))) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
