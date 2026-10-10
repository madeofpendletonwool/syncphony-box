package daemon

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
)

// lanMux serves the phone-facing setup form on the LAN — and only while the
// window is open (the box is unconfigured, or the window was opened with
// SIGUSR1, later the OK long press). It never offers anything beyond
// setting the server URL and the box's name.
func (d *Daemon) lanMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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

	pairs := []config.KV{{Key: "server_url", Value: serverURL}}
	if name != "" {
		pairs = append(pairs, config.KV{Key: "name", Value: name})
	}
	if err := config.SetKeys(d.paths.Txt, pairs...); err != nil {
		d.renderFormError(w, "Couldn't save the setting on the box's boot partition. Try again, or edit syncphony.txt on the SD card.")
		d.log.Printf("boxd: setup: save failed: %v", err)
		return
	}
	d.log.Printf("boxd: setup: server_url=%s saved from %s", serverURL, r.RemoteAddr)

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
	if err := d.run("systemctl", "restart", "syncphony-kiosk.service"); err != nil {
		d.log.Printf("boxd: setup: kiosk restart failed: %v", err)
	}

	writePage(w, savedPageHTML(serverURL))
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
