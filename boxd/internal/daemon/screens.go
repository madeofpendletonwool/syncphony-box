package daemon

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"

	qrcode "github.com/skip2/go-qrcode"
)

// handleStatus reports everything the local screens (and tests) need.
func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	server := d.serverURL()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"configured": server != "",
		"server":     server,
		"reachable":  d.serverReachable(),
		"target":     d.targetURL(),
		"version":    d.version,
		"name":       d.displayName(),
		"hostname":   hostname(),
		"ips":        boxIPs(),
		"network":    networkState(),
	})
}

func hostname() string {
	if hn, err := os.Hostname(); err == nil {
		return hn
	}
	return ""
}

// handleSetupScreen serves the TV setup page: a big QR code and
// http://<hostname>.local, which the phone opens to set the server URL.
func (d *Daemon) handleSetupScreen(w http.ResponseWriter, r *http.Request) {
	if d.configured() {
		// The kiosk only lands here before the first navigate succeeds; if
		// the server URL is set, the start page is the right entry.
		http.Redirect(w, r, "/start", http.StatusFound)
		return
	}
	setupURL := "http://" + hostname() + ".local"
	data, err := qrPNG(setupURL)
	if err != nil {
		d.log.Printf("boxd: qr: %v", err)
	}
	writePage(w, setupScreenHTML(setupScreenData{
		QR:      data,
		URL:     setupURL,
		IPs:     boxIPs(),
		Network: networkState(),
	}))
}

// handleStartScreen serves the page the kiosk opens when a server is
// configured: it checks the server before navigating, so the box never
// shows Chromium's error page — it shows the offline screen and returns to
// /tv on its own once /healthz answers.
func (d *Daemon) handleStartScreen(w http.ResponseWriter, r *http.Request) {
	if !d.configured() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	st := startScreenData{
		Server:  d.serverURL(),
		Target:  d.targetURL(),
		IPs:     boxIPs(),
		Network: networkState(),
	}
	writePage(w, startScreenHTML(st))
}

func qrPNG(content string) (string, error) {
	png, err := qrcode.Encode(content, qrcode.Medium, 560)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}
