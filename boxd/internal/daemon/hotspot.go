package daemon

import (
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

	dataPersist = "/usr/lib/syncphony-box/data-persist"
)

// Waits in the join and recheck flows; vars so tests run them instantly.
var (
	joinDelay     = 1 * time.Second  // let the phone get its answer before the AP goes away
	recheckWait   = 45 * time.Second // how long NetworkManager gets to find a saved network
	recheckPoll   = 3 * time.Second
	healthzTries  = 6 // after a join: DHCP, DNS and the clock need a moment
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
	if v.online || v.offlineSince.IsZero() {
		return netNothing, ""
	}
	grace := bootGrace
	if v.seenOnline {
		grace = lostGrace
	}
	if v.now.Sub(v.offlineSince) >= grace {
		return netStartAP, fmt.Sprintf("no Ethernet or Wi-Fi connection for %s", grace)
	}
	return netNothing, ""
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
		if err == nil {
			d.mu.Lock()
			d.net.device = dev
			d.mu.Unlock()
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
		d.net.offlineSince = time.Now() // re-arm the grace instead of retrying every tick
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	d.net.phase = phaseHotspot
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

// joinWiFi is the save from the portal, run after the phone got its
// answer: give the radio back, join, and on success persist the network
// and (if the form had it) the server URL. On failure the hotspot comes
// back and the TV shows why.
func (d *Daemon) joinWiFi(ssid, password string, hidden bool, serverURL, name string) {
	d.netOp.Lock()
	defer d.netOp.Unlock()
	time.Sleep(joinDelay)

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
		d.mu.Unlock()
		d.startHotspot("the join failed")
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
	for i := 0; i < healthzTries; i++ {
		if i > 0 {
			time.Sleep(healthzRetry)
		}
		if checkHealthz(serverURL) {
			if err := d.saveServer(serverURL, name, "the Wi-Fi setup"); err == nil {
				return
			}
			break
		}
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
	ssid := strings.TrimSpace(r.PostFormValue("other_ssid"))
	if ssid == "" {
		ssid = r.PostFormValue("ssid")
	}
	password := r.PostFormValue("password") // spaces are legal in a passphrase
	serverURL := strings.TrimSpace(r.PostFormValue("server_url"))
	name := strings.TrimSpace(r.PostFormValue("name"))

	if ssid == "" {
		fail("Pick your Wi-Fi network, or type its name.")
		return
	}
	d.mu.Lock()
	known, secure := false, false
	for _, nw := range d.net.networks {
		if nw.SSID == ssid {
			known, secure = true, nw.Secure
		}
	}
	askServer := d.cfg.ServerURL == ""
	d.mu.Unlock()
	if secure && password == "" {
		fail(ssid + " needs its Wi-Fi password.")
		return
	}
	if password != "" && (len(password) < 8 || len(password) > 64) {
		fail("Wi-Fi passwords are 8 to 63 characters — check it and try again.")
		return
	}
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
	go d.joinWiFi(ssid, password, !known, serverURL, name)
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

// wifiStatus is the hotspot's part of /status.
func (d *Daemon) wifiStatus() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]any{
		"phase":   string(d.net.phase),
		"hotspot": d.net.apSSID,
		"joining": d.net.joining,
		"error":   d.net.lastError,
		"notice":  d.net.notice,
	}
}
