package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"
)

// bridgeMux serves everything Chromium-facing on loopback: the /v1 box
// bridge (ADR 0016) plus the local screens and status used by the kiosk.
func (d *Daemon) bridgeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/info", d.guard(d.handleInfo))
	mux.HandleFunc("POST /v1/events", d.guard(d.handleEvents))
	mux.HandleFunc("POST /v1/heartbeat", d.guard(d.handleHeartbeat))
	mux.HandleFunc("POST /v1/reboot", d.guard(d.handleReboot))
	mux.HandleFunc("POST /v1/reload", d.guard(d.handleReload))
	mux.HandleFunc("OPTIONS /v1/", d.guard(func(w http.ResponseWriter, r *http.Request) {
		// The preflight — including Chromium's Private Network Access
		// preflight before an HTTPS page posts to loopback — is answered by
		// guard's CORS headers.
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /status", d.handleStatus)
	mux.HandleFunc("GET /setup", d.handleSetupScreen)
	mux.HandleFunc("GET /start", d.handleStartScreen)
	return mux
}

// allowedHost: the bridge only answers when addressed as loopback (on its
// own port), so a DNS-rebound name can't reach it even with a spoofed
// Origin.
func allowedHost(host string) bool {
	bridge, _ := listenAddrs()
	_, port, err := net.SplitHostPort(bridge)
	if err != nil || port == "" {
		port = "8099"
	}
	for _, h := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if strings.EqualFold(host, h) || strings.EqualFold(host, net.JoinHostPort(h, port)) {
			return true
		}
	}
	return false
}

// serverOrigin is the configured server's origin (scheme://host[:port]) —
// the only Origin the bridge accepts (ADR 0016). Empty when unconfigured.
func (d *Daemon) serverOrigin() string {
	server := d.serverURL()
	if server == "" {
		return ""
	}
	u := server
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		return ""
	}
	scheme := "https"
	if strings.HasPrefix(server, "http://") {
		scheme = "http"
	}
	return scheme + "://" + strings.ToLower(u)
}

func (d *Daemon) originAllowed(origin string) bool {
	want := d.serverOrigin()
	return want != "" && strings.ToLower(strings.TrimSuffix(origin, "/")) == want
}

// guard wraps bridge handlers with the Host check, the Origin check and the
// CORS (incl. Private Network Access) answers ADR 0016 requires. Requests
// from any origin but the configured server's are refused.
func (d *Daemon) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) {
			d.log.Printf("boxd: bridge: refused host %q", r.Host)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && !d.originAllowed(origin) {
			d.log.Printf("boxd: bridge: refused origin %q", origin)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		next(w, r)
	}
}

func (d *Daemon) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"version":      d.version,
		"name":         d.displayName(),
		"capabilities": []string{"reboot", "reload"}, // cec arrives in stage 3
	})
}

var eventTypes = map[string]bool{
	"playing": true, "paused": true, "idle": true, "paired": true, "unpaired": true,
}

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var ev struct {
		Type   string  `json:"type"`
		RoomID *string `json:"roomId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil || !eventTypes[ev.Type] {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	room := ""
	if ev.RoomID != nil {
		room = *ev.RoomID
	}
	d.mu.Lock()
	d.lastEvent, d.lastEventAt = ev.Type, time.Now()
	d.mu.Unlock()
	// Stage 2: events are logged (CEC and the watchdog consume them in
	// stage 3).
	d.log.Printf("boxd: event type=%s room=%s", ev.Type, room)
	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.lastBeat = time.Now()
	d.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleReboot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"ok":true}` + "\n"))
	go func() {
		time.Sleep(250 * time.Millisecond) // let the response flush
		if err := d.run("systemctl", "reboot"); err != nil {
			d.log.Printf("boxd: reboot failed: %v", err)
		}
	}()
}

func (d *Daemon) handleReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"ok":true}` + "\n"))
	go func() {
		time.Sleep(250 * time.Millisecond)
		if err := d.run("systemctl", "restart", "syncphony-kiosk.service"); err != nil {
			d.log.Printf("boxd: reload failed: %v", err)
		}
	}()
}
