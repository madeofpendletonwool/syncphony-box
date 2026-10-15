package daemon

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/wifi"
)

// lanMux serves the phone-facing setup form on the LAN — and only while the
// window is open (the box is unconfigured, or the window was opened with
// SIGUSR1 or the OK long press). It never offers anything beyond setting
// the server URL and the box's name — or, while the box is its own setup
// hotspot, the house Wi-Fi (MAD-808).
func (d *Daemon) lanMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if d.hotspotUp() {
			// No network at all: the box is its own Wi-Fi network and
			// this listener is its captive portal (MAD-808).
			d.servePortal(w, r)
			return
		}
		if !d.setupWindowOpen() {
			w.WriteHeader(http.StatusNotFound)
			writePage(w, closedPageHTML)
			return
		}
		switch {
		case r.URL.Path == "/" && r.Method == http.MethodGet:
			writePage(w, phoneFormHTML(phoneFormData{
				Name: d.displayName(), // current name as the starting point
			}))
		case r.URL.Path == "/setup" && r.Method == http.MethodPost:
			d.handleSetupSave(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func (d *Daemon) handleSetupSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	serverURL := strings.TrimSpace(r.PostFormValue("server_url"))
	name := strings.TrimSpace(r.PostFormValue("name"))

	if serverURL == "" {
		d.renderFormError(w, "Enter your Syncphony server's address.")
		return
	}
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		d.renderFormError(w, "That doesn't look like a server address — it should be like https://syncphony.example.com")
		return
	}

	// Validate from the box: the server must answer /healthz before the
	// URL is saved, so a typo never strands the box on the offline screen.
	if !checkHealthz(serverURL) {
		d.renderFormError(w, fmt.Sprintf("Can't reach %s — check the address and that your Syncphony server is up.", serverURL))
		return
	}

	if err := d.saveServer(serverURL, name, r.RemoteAddr); err != nil {
		d.renderFormError(w, "Couldn't save the setting on the box's boot partition. Try again, or edit syncphony.txt on the SD card.")
		return
	}
	writePage(w, savedPageHTML(serverURL))
}

// saveServer writes server_url (and name=, when given) to syncphony.txt
// and hands the TV to the server. The caller has validated the URL and
// checked its /healthz; from says who asked, for the log.
func (d *Daemon) saveServer(serverURL, name, from string) error {
	pairs := []config.KV{{Key: "server_url", Value: serverURL}}
	if name != "" {
		pairs = append(pairs, config.KV{Key: "name", Value: name})
	}
	// syncphony.txt sits on the read-only boot partition (ADR 0007): the
	// save goes through the remount dance (rw, write, sync, ro).
	err := config.WithMountWritable(d.paths.Txt, d.run, d.log, func() error {
		return config.SetKeys(d.paths.Txt, pairs...)
	})
	if err != nil {
		d.log.Printf("boxd: setup: save failed: %v", err)
		return err
	}
	d.log.Printf("boxd: setup: server_url=%s saved from %s", serverURL, from)

	// syncphony.txt is the source of truth: re-read it, refresh config.env
	// for the kiosk, apply a new name= to the hostname, and hand the TV to
	// the server by restarting the kiosk through the local start page.
	d.reloadConfig()
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	if err := config.WriteEnvFile(d.paths.Env, cfg); err != nil {
		d.log.Printf("boxd: setup: cannot write %s: %v", d.paths.Env, err)
	}
	if name != "" && cfg.Hostname != "" && cfg.Hostname != hostname() {
		if err := d.run("hostnamectl", "set-hostname", cfg.Hostname); err != nil {
			d.log.Printf("boxd: setup: hostname: %v", err)
		}
		d.run("systemctl", "try-restart", "avahi-daemon.service")
	}
	d.closeSetupWindow()
	d.restartKiosk("setup saved; entering the server through the start page", false)
	return nil
}

func (d *Daemon) renderFormError(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusBadRequest)
	writePage(w, phoneFormHTML(phoneFormData{Name: d.displayName(), Error: message}))
}

type phoneFormData struct {
	Name  string
	Error string
}

const formStyle = `
	html { height: 100%; }
	body {
		margin: 0; min-height: 100%;
		background: #101014; color: #f4f4f5;
		font-family: system-ui, sans-serif;
		-webkit-text-size-adjust: 100%;
	}
	main { max-width: 28rem; margin: 0 auto; padding: 2.5rem 1.5rem; }
	h1 { font-size: 1.8rem; margin: 0 0 .5rem; }
	p { color: #8b8b94; line-height: 1.5; }
	label { display: block; margin: 1.5rem 0 .4rem; font-weight: 600; }
	input {
		width: 100%; box-sizing: border-box;
		font-size: 1.1rem; padding: .8rem 1rem;
		background: #1c1c22; color: #f4f4f5;
		border: 1px solid #2c2c34; border-radius: .8rem;
	}
	input:focus { outline: 2px solid oklch(0.65 0.16 295); }
	button {
		margin-top: 1.75rem; width: 100%;
		font-size: 1.1rem; font-weight: 600; padding: .9rem 1rem;
		color: oklch(0.98 0.01 295); background: oklch(0.45 0.112 295);
		border: 0; border-radius: .8rem;
	}
	.error {
		margin-top: 1rem; padding: .8rem 1rem;
		background: #2a1518; border: 1px solid #5c2a30; border-radius: .8rem;
		color: #f1b8ba;
	}
`

func phoneFormHTML(d phoneFormData) string {
	b := &strings.Builder{}
	t := template.Must(template.New("form").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Set up your Syncphony Box</title>
<style>` + formStyle + `</style>
</head>
<body>
<main>
	<h1>Set up this box</h1>
	<p>Which Syncphony server should it show?</p>
	{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
	<form method="post" action="/setup">
		<label for="server_url">Your Syncphony server</label>
		<input id="server_url" name="server_url" type="url" inputmode="url"
			autocomplete="off" placeholder="https://syncphony.example.com" required>
		<label for="name">Name for this box (optional)</label>
		<input id="name" name="name" type="text" value="{{.Name}}"
			autocomplete="off" placeholder="Living room TV">
		<button type="submit">Save</button>
	</form>
</main>
</body>
</html>`))
	t.Execute(b, d)
	return b.String()
}

func savedPageHTML(server string) string {
	b := &strings.Builder{}
	t := template.Must(template.New("saved").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Saved</title>
<style>` + formStyle + `</style>
</head>
<body>
<main>
	<h1>Saved</h1>
	<p>This box now shows <b style="color:#f4f4f5">{{.}}</b>.</p>
	<p>The TV is on its way there — finish by pairing it from your Syncphony app.</p>
</main>
</body>
</html>`))
	t.Execute(b, server)
	return b.String()
}

const closedPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Syncphony Box</title>
<style>` + formStyle + `</style>
</head>
<body>
<main>
	<h1>This box is already set up</h1>
	<p>To point it at another server, edit syncphony.txt on its SD card.</p>
</main>
</body>
</html>`

// --- the captive portal (MAD-808) ---------------------------------------------

type wifiFormData struct {
	APSSID    string
	Networks  []wifi.Network
	AskServer bool // the box has no server_url yet: the form asks for it too
	Name      string
	Error     string
}

// wifiFormHTML is the portal page a phone opens on the box's setup
// network: the house Wi-Fi (scanned list, or typed for a hidden one),
// its password, and the server URL when the box doesn't have one yet.
func wifiFormHTML(d wifiFormData) string {
	b := &strings.Builder{}
	t := template.Must(template.New("wifi").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Set up your Syncphony Box</title>
<style>` + formStyle + `
	select {
		width: 100%; box-sizing: border-box;
		font-size: 1.1rem; padding: .8rem 1rem;
		background: #1c1c22; color: #f4f4f5;
		border: 1px solid #2c2c34; border-radius: .8rem;
	}
	.show { display: flex; gap: .5rem; align-items: center; margin-top: .6rem; color: #8b8b94; font-weight: 400; }
	.show input { width: auto; }
	.hint { font-size: .9rem; margin: .4rem 0 0; }
</style>
</head>
<body>
<main>
	<h1>Connect this box to Wi-Fi</h1>
	<p>Pick the Wi-Fi this box should use. Your phone will leave
	{{if .APSSID}}{{.APSSID}}{{else}}the box's setup network{{end}} when you save.</p>
	{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
	<form method="post" action="/wifi">
		<label for="ssid">Wi-Fi network</label>
		<select id="ssid" name="ssid">
			{{range .Networks}}<option value="{{.SSID}}">{{.SSID}}{{if .Secure}} 🔒{{end}}</option>
			{{end}}<option value="">Not listed…</option>
		</select>
		<label for="other_ssid">Network not listed? Type its name</label>
		<input id="other_ssid" name="other_ssid" type="text" autocomplete="off"
			autocapitalize="off" autocorrect="off" spellcheck="false" placeholder="My hidden network">
		<label for="password">Wi-Fi password</label>
		<input id="password" name="password" type="password" autocomplete="off"
			autocapitalize="off" autocorrect="off" spellcheck="false">
		<label class="show"><input type="checkbox"
			onchange="document.getElementById('password').type = this.checked ? 'text' : 'password'">
			Show password</label>
		{{if .AskServer}}
		<label for="server_url">Your Syncphony server</label>
		<input id="server_url" name="server_url" type="url" inputmode="url"
			autocomplete="off" placeholder="https://syncphony.example.com">
		<p class="hint">Optional — you can also set it later from a phone on your Wi-Fi.</p>
		<label for="name">Name for this box (optional)</label>
		<input id="name" name="name" type="text" value="{{.Name}}"
			autocomplete="off" placeholder="Living room TV">
		{{end}}
		<button type="submit">Connect</button>
	</form>
</main>
</body>
</html>`))
	t.Execute(b, d)
	return b.String()
}

func joiningPageHTML(ssid, apSSID string) string {
	b := &strings.Builder{}
	t := template.Must(template.New("joining").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Connecting</title>
<style>` + formStyle + `</style>
</head>
<body>
<main>
	<h1>Connecting to {{.SSID}}</h1>
	<p>Your phone will now leave {{if .AP}}{{.AP}}{{else}}the box's setup network{{end}} — that's expected.
	Watch the TV: it shows when the box is on your Wi-Fi.</p>
	<p>If it can't join, the setup network comes back and the TV says why —
	join it again to retry.</p>
</main>
</body>
</html>`))
	t.Execute(b, struct{ SSID, AP string }{ssid, apSSID})
	return b.String()
}
