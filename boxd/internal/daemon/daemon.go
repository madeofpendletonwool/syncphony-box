// Package daemon is boxd's running half: the box bridge on loopback
// (ADR 0016), the local screens the kiosk shows when there's no server
// to display — setup with a QR code, and the offline "can't reach"
// screen — and the watchdogs and thermals that make the box heal itself
// (MAD-806, ADR 0008).
package daemon

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/cec"
	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
)

const (
	bridgeAddr   = "127.0.0.1:8099" // ADR 0016: loopback only
	lanAddr      = ":80"            // the setup form, open only while unconfigured
	setupWindow  = 10 * time.Minute // after SIGUSR1 or a long OK press on the remote
	infoWindow   = 30 * time.Second // how long the info screen holds the TV after that press
	healthzCache = 2 * time.Second

	// Pairing durability (MAD-804, ADR 0006): Chromium writes its cookie
	// store and localStorage lazily, so the writes that make this box stay
	// paired sit in memory and the page cache for a while. After the
	// pairing-critical bridge events, boxd syncs the disks in a short
	// burst — the first pass flushes what Chromium has already committed,
	// the later ones catch the commits it makes seconds after the event —
	// so a power pull 10 s after pairing can't lose the cookie or the
	// speaker flag. Playing (the speaker localStorage write happens around
	// it) flushes too, but at most this often.
	playingFlushEvery = 5 * time.Minute
)

// flushDelays spaces the sync() burst after a pairing-critical event: it
// must outlast Chromium's own commit lag, and it costs nothing (an idle
// sync on a Pi is milliseconds).
var flushDelays = []time.Duration{0, 5 * time.Second, 15 * time.Second, 30 * time.Second}

var healthzClient = &http.Client{
	Timeout: 4 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	},
}

// listenAddrs resolves the listen addresses: the fixed box defaults, or the
// BOXD_BRIDGE_ADDR / BOXD_LAN_ADDR dev overrides (same spirit as the SB_*
// config hooks — a machine with those ports busy can still run the daemon).
func listenAddrs() (bridge, lan string) {
	if b := os.Getenv("BOXD_BRIDGE_ADDR"); b != "" {
		bridge = b
	} else {
		bridge = bridgeAddr
	}
	if l := os.Getenv("BOXD_LAN_ADDR"); l != "" {
		lan = l
	} else {
		lan = lanAddr
	}
	return bridge, lan
}

// Daemon is boxd's shared state.
type Daemon struct {
	mu      sync.Mutex
	cfg     config.Config
	version string
	log     *log.Logger
	run     config.RunCommand
	paths   config.Paths

	// syncDisk is the disk-flush used by the pairing-durability burst;
	// a func field so tests can count calls instead of syncing.
	syncDisk func() error

	// vcgencmd samples the hardware thermals; a func field so tests can
	// feed readings without a Pi.
	vcgencmd func(args ...string) (string, error)

	startedAt      time.Time // boxd start; watchdog graces count from here
	setupUntil     time.Time // LAN setup window open until then (zero = closed)
	infoUntil      time.Time // the info screen holds /start until then (zero = off)
	cec            *cec.Controller
	cecUp          bool        // the CEC adapters registered; drives /v1/info's capabilities
	wakeTV         func()      // the CEC wake-and-switch; a func field so tests can count calls
	lastBeat       time.Time   // last /v1/heartbeat from the page
	lastEvent      string      // last /v1/events type (the nightly reload waits out "playing")
	lastEventAt    time.Time   // when it arrived
	lastOK         bool        // last /healthz answer
	lastAt         time.Time   // when it was checked
	offlineSince   time.Time   // server unreachable continuously since
	lastRestart    time.Time   // last kiosk restart boxd performed (settle window anchor)
	restarts       []time.Time // watchdog-caused kiosk restarts inside watchdogWindow
	lastNightlyDay string      // local day the nightly reload last ran ("2006-01-02")
	therm          thermals    // last thermal sample (zero = none yet)
	lastFlush      time.Time   // last pairing-durability sync burst
}

// New builds a daemon around the current syncphony.txt (the SB_* env hooks
// from the config package apply).
func New(version string, log *log.Logger, run config.RunCommand) *Daemon {
	d := &Daemon{
		version:   version,
		log:       log,
		run:       run,
		paths:     config.PathsFromEnv(),
		startedAt: time.Now(),
		vcgencmd:  realVcgencmd,
		syncDisk: func() error {
			syscall.Sync()
			return nil
		},
	}
	d.reloadConfig()
	d.cec = cec.New(d.cecConfig(d.cfg), log, d.onRemoteLongPress)
	d.wakeTV = d.cec.WakeAndSwitch
	return d
}

// cecConfig is the CEC slice of syncphony.txt for the controller, built
// once at startup (cec= and its neighbors are boot settings; a change
// lands on the next boot, like resolution=).
func (d *Daemon) cecConfig(cfg config.Config) cec.Config {
	return cec.Config{
		Enabled:      cfg.CEC == "on",
		SecondHDMI:   cfg.Audio == "hdmi2",
		Standby:      cfg.CECStandby == "on",
		StandbyAfter: time.Duration(cfg.CECStandbyMinutes) * time.Minute,
	}
}

// reloadConfig re-reads syncphony.txt and updates everything derived from
// it. Called at start and after the setup form saves.
func (d *Daemon) reloadConfig() {
	cfg := config.ParseFile(d.paths.Txt)
	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
	for _, w := range cfg.Warnings {
		d.log.Printf("boxd: config: %s", w)
	}
}

func (d *Daemon) serverURL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.ServerURL
}

func (d *Daemon) configured() bool { return d.serverURL() != "" }

// displayName is what the box calls itself: name= from syncphony.txt, the
// hostname, or a sensible default.
func (d *Daemon) displayName() string {
	d.mu.Lock()
	name := d.cfg.Name
	d.mu.Unlock()
	if name != "" {
		return name
	}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		return hn
	}
	return "Syncphony Box"
}

// targetURL is the /tv URL the box opens on the server (ADR 0016).
func (d *Daemon) targetURL() string {
	return config.TargetURL(d.serverURL(), d.version, d.displayName())
}

// Run serves the bridge and the screens until SIGTERM.
func (d *Daemon) Run() error {
	// The addresses are fixed on a box (ADR 0016: loopback 8099; the setup
	// form on 80); BOXD_BRIDGE_ADDR / BOXD_LAN_ADDR only exist so a dev
	// machine with those ports busy can still run the daemon.
	bridgeAddr, lanAddr := listenAddrs()
	bridgeSrv := &http.Server{
		Addr:    bridgeAddr,
		Handler: d.bridgeMux(),
	}
	lanSrv := &http.Server{
		Addr:    lanAddr,
		Handler: d.lanMux(),
	}

	ln, err := net.Listen("tcp", bridgeAddr)
	if err != nil {
		return err
	}
	go func() {
		if err := bridgeSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			d.log.Printf("boxd: bridge server: %v", err)
		}
	}()
	lanLn, err := net.Listen("tcp", lanAddr)
	if err != nil {
		// Not fatal: the bridge and the kiosk screens still work; only the
		// phone setup form is unavailable (something else holds :80).
		d.log.Printf("boxd: cannot listen on %s (setup form unavailable): %v", lanAddr, err)
	} else {
		d.log.Printf("boxd: setup form on %s (%s)", lanAddr, d.windowState())
		go func() {
			if err := lanSrv.Serve(lanLn); err != nil && err != http.ErrServerClosed {
				d.log.Printf("boxd: lan server: %v", err)
			}
		}()
	}

	d.log.Printf("boxd: %s bridge on http://%s (server: %s)", d.version, bridgeAddr, d.serverOrNone())
	go d.watch()
	go d.sampleThermalsLoop()
	go func() {
		// CEC (MAD-807): register as a playback device on the adapters
		// and watch the bus. cec=off or no /dev/cec leaves this inert.
		n := d.cec.Start()
		d.mu.Lock()
		d.cecUp = n > 0
		d.mu.Unlock()
	}()

	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR1, syscall.SIGUSR2)
	for s := range sig {
		switch s {
		case syscall.SIGUSR1:
			// Stage 3 wires this to a long OK press on the remote; for now
			// it's the manual way to reopen the setup window.
			d.mu.Lock()
			d.setupUntil = time.Now().Add(setupWindow)
			d.mu.Unlock()
			d.log.Printf("boxd: setup window open for %s", setupWindow)
		case syscall.SIGUSR2:
			d.mu.Lock()
			d.setupUntil = time.Time{}
			d.mu.Unlock()
			d.log.Printf("boxd: setup window closed")
		default:
			bridgeSrv.Close()
			lanSrv.Close()
			return nil
		}
	}
	return nil
}

func (d *Daemon) serverOrNone() string {
	if u := d.serverURL(); u != "" {
		return u
	}
	return "not configured"
}

// setupWindowOpen reports whether the LAN setup form may be served: the box
// is unconfigured, or the window was opened (SIGUSR1, later the OK long
// press).
func (d *Daemon) setupWindowOpen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfg.ServerURL == "" {
		return true
	}
	return time.Now().Before(d.setupUntil)
}

func (d *Daemon) closeSetupWindow() {
	d.mu.Lock()
	d.setupUntil = time.Time{}
	d.mu.Unlock()
}

// onRemoteLongPress is the box's one remote gesture (MAD-807): OK held
// for 3 seconds on the TV remote. It opens the setup window — the phone
// form at http://<hostname>.local — and puts the box's IP and version on
// the TV by restarting the kiosk onto the info screen; the page goes back
// to the room by itself (and the speaker resumes, MAD-804).
func (d *Daemon) onRemoteLongPress() {
	now := time.Now()
	d.mu.Lock()
	d.setupUntil = now.Add(setupWindow)
	d.infoUntil = now.Add(infoWindow)
	d.mu.Unlock()
	d.log.Printf("boxd: cec: OK long-press: setup window open for %s; showing box info", setupWindow)
	d.restartKiosk("box info (OK long press)", false)
}

// infoWindowOpen reports whether /start should show the box's info
// screen instead of connecting (the long-press set it).
func (d *Daemon) infoWindowOpen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Now().Before(d.infoUntil)
}

func (d *Daemon) windowState() string {
	if d.setupWindowOpen() {
		return "open — box unconfigured"
	}
	return "closed — box configured"
}

// serverReachable checks the configured server's /healthz, caching the
// answer briefly so /start's polling and the watch don't hammer it.
func (d *Daemon) serverReachable() bool {
	server := d.serverURL()
	if server == "" {
		return false
	}
	d.mu.Lock()
	lastOK, lastAt := d.lastOK, d.lastAt
	d.mu.Unlock()
	if time.Since(lastAt) < healthzCache {
		return lastOK
	}
	ok := checkHealthz(server)
	d.mu.Lock()
	d.lastOK, d.lastAt = ok, time.Now()
	d.mu.Unlock()
	return ok
}

func checkHealthz(server string) bool {
	resp, err := healthzClient.Get(strings.TrimSuffix(server, "/") + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// flushDisk makes the pairing state durable: a sync() burst after the
// events that write it (see flushDelays above). paired/unpaired — the
// cookie — always flush; playing — the speaker localStorage flag around
// playback start — flushes at most once per playingFlushEvery so a long
// night of music doesn't sync on every song.
func (d *Daemon) flushDisk(event string) {
	d.mu.Lock()
	now := time.Now()
	if event != "paired" && event != "unpaired" {
		if now.Sub(d.lastFlush) < playingFlushEvery {
			d.mu.Unlock()
			return
		}
	}
	d.lastFlush = now
	syncDisk := d.syncDisk
	d.mu.Unlock()
	d.log.Printf("boxd: %s event; syncing disks so the pairing survives a power cut", event)
	go func() {
		for _, delay := range flushDelays {
			if delay > 0 {
				time.Sleep(delay)
			}
			if err := syncDisk(); err != nil {
				d.log.Printf("boxd: sync: %v", err)
			}
		}
	}()
}

// storageHealthy reports whether /data — the only partition that survives
// the overlay (MAD-805, ADR 0007) — is mounted. Without it the box boots
// anyway, but the pairing, the applied config and the logs are gone (they
// live on tmpfs for the boot), so the kiosk shows the storage-lost screen
// instead of pretending everything is fine. SB_STORAGE pins the answer in
// dev and tests ("ok"/"missing"), like the other SB_* hooks.
func storageHealthy() bool {
	if v := os.Getenv("SB_STORAGE"); v != "" {
		return v != "missing"
	}
	return config.Mounted(config.DefaultDataDir)
}

// boxIPs lists the box's LAN addresses (the offline screen shows them, so
// whoever is setting the box up can reach the setup form by IP if mDNS
// doesn't work on their phone).
func boxIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			if v4 := ipnet.IP.To4(); v4 != nil {
				ips = append(ips, v4.String())
			}
		}
	}
	return ips
}

// networkState reports whether the box has a default route at all — the
// offline screen distinguishes "no network" from "server down".
func networkState() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "00000000" {
			return "online"
		}
	}
	if len(boxIPs()) > 0 {
		return "no default route"
	}
	return "offline"
}
