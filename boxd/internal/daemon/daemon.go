// Package daemon is boxd's running half: the box bridge on loopback
// (ADR 0016) and the local screens the kiosk shows when there's no server
// to display — setup with a QR code, and the offline "can't reach" screen.
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

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
)

const (
	bridgeAddr   = "127.0.0.1:8099" // ADR 0016: loopback only
	lanAddr      = ":80"            // the setup form, open only while unconfigured
	setupWindow  = 10 * time.Minute // after SIGUSR1 (stage 3 wires the OK long-press)
	healthzCache = 2 * time.Second

	// The offline screen takes over when the server has been unreachable
	// for this long while the page is alive (heartbeats fresh): the kiosk is
	// restarted through the local start page, which shows the offline
	// screen and returns to /tv on its own once /healthz answers. After the
	// restart there are no heartbeats (the page is local), so this cannot
	// loop; the cooldown is a second guard.
	offlineGrace      = 2 * time.Minute
	heartbeatFresh    = 90 * time.Second
	kioskRestartGuard = 10 * time.Minute
)

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

	setupUntil   time.Time // LAN setup window open until then (zero = closed)
	lastBeat     time.Time // last /v1/heartbeat from the page
	lastEvent    string
	lastEventAt  time.Time
	lastOK       bool      // last /healthz answer
	lastAt       time.Time // when it was checked
	offlineSince time.Time // server unreachable continuously since
	lastRestart  time.Time // last kiosk restart triggered by the offline watch
}

// New builds a daemon around the current syncphony.txt (the SB_* env hooks
// from the config package apply).
func New(version string, log *log.Logger, run config.RunCommand) *Daemon {
	d := &Daemon{
		version: version,
		log:     log,
		run:     run,
		paths:   config.PathsFromEnv(),
	}
	d.reloadConfig()
	return d
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

// watch is the background loop: the offline screen trigger. While the page
// is alive (fresh heartbeats) but the server stays unreachable past the
// grace period, restart the kiosk so it re-enters through the local start
// page — which shows the offline screen and returns on its own.
func (d *Daemon) watch() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for range tick.C {
		if !d.configured() {
			continue
		}
		ok := d.serverReachable()
		d.mu.Lock()
		now := time.Now()
		if ok {
			d.offlineSince = time.Time{}
		} else if d.offlineSince.IsZero() {
			d.offlineSince = now
		}
		restart := !d.offlineSince.IsZero() &&
			now.Sub(d.offlineSince) >= offlineGrace &&
			!d.lastBeat.IsZero() && now.Sub(d.lastBeat) < heartbeatFresh &&
			(d.lastRestart.IsZero() || now.Sub(d.lastRestart) > kioskRestartGuard)
		if restart {
			d.lastRestart = now
		}
		d.mu.Unlock()
		if restart {
			d.log.Printf("boxd: server unreachable for %s with a live page; restarting the kiosk onto the offline screen", offlineGrace)
			if err := d.run("systemctl", "restart", "syncphony-kiosk.service"); err != nil {
				d.log.Printf("boxd: kiosk restart failed: %v", err)
			}
		}
	}
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
