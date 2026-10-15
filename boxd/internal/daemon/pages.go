package daemon

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
)

// The pages share Syncphony's look: near-black, large type, the violet
// accent from the web app's default palette. Self-contained — no external
// assets — because the box is often offline when these show.

var pageStyle = `
	html { height: 100%; }
	body {
		margin: 0; min-height: 100%;
		display: grid; place-items: center;
		background: #101014; color: #f4f4f5;
		font-family: system-ui, sans-serif;
		cursor: none;
	}
	main { max-width: 60rem; padding: 3rem; text-align: center; }
	h1 { font-size: 3rem; margin: 0 0 1rem; }
	p { font-size: 1.5rem; line-height: 1.5; margin: 0.75rem 0; }
	code {
		font-family: ui-monospace, monospace; font-size: 1.25rem;
		background: #1c1c22; padding: 0.1rem 0.4rem; border-radius: 0.3rem;
	}
	.dim { color: #8b8b94; font-size: 1.1rem; }
	.qr {
		background: #fff; padding: 1.5rem; border-radius: 1rem;
		margin: 2rem auto; width: 28rem; max-width: 80vw;
	}
	.qr img { width: 100%; image-rendering: pixelated; }
`

type setupScreenData struct {
	QR      string
	URL     string
	IPs     []string
	Network string
}

func setupScreenHTML(d setupScreenData) string {
	ips := make([]string, len(d.IPs))
	for i, ip := range d.IPs {
		ips[i] = "http://" + ip
	}
	b := &strings.Builder{}
	t := template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Syncphony Box — set up</title>
<style>` + pageStyle + `</style>
</head>
<body>
<main>
	<h1>Set up this box</h1>
	<p>On your phone, joined to this network, scan the code —</p>
	<div class="qr">{{if .QR}}<img src="{{.QR}}" alt="QR code">{{end}}</div>
	<p>or open <b>{{.URL}}</b></p>
	{{if .IPs}}<p class="dim">or try {{range .IPs}}<code>{{.}}</code> {{end}}</p>{{end}}
	<p class="dim">There you can set which Syncphony server this box shows.</p>
</main>
</body>
</html>`))
	t.Execute(b, struct {
		QR  template.URL // boxd generates this data URI itself; html/template would otherwise rewrite it
		URL string
		IPs []string
	}{template.URL(d.QR), d.URL, ips})
	return b.String()
}

type startScreenData struct {
	Server  string
	Target  string
	IPs     []string
	Network string
}

type infoScreenData struct {
	Name    string
	Version string
	Server  string
	Target  string
	IPs     []string
	Network string
}

// infoScreenHTML is what a 3-second OK hold on the TV remote shows
// (MAD-807): the box's name, version, IPs and server, big enough to read
// from the couch, while the setup window is open for a phone. It holds
// the TV for a few seconds, then navigates to the room the same way the
// start screen does.
func infoScreenHTML(d infoScreenData) string {
	targetJS, err := json.Marshal(d.Target)
	if err != nil {
		targetJS = []byte(`""`)
	}
	b := &strings.Builder{}
	t := template.Must(template.New("info").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Syncphony Box — info</title>
<style>` + pageStyle + `
	.count { font-variant-numeric: tabular-nums; }
</style>
</head>
<body>
<main>
	<h1>{{.Name}}</h1>
	<p>Syncphony Box <b>{{.Version}}</b></p>
	{{if .IPs}}<p class="dim">On this network: {{range .IPs}}<code>{{.}}</code> {{end}}</p>{{end}}
	<p class="dim">Showing <b>{{.Server}}</b> · network: {{.Network}}</p>
	<p class="dim">Setup is open for ten minutes: set this box from a phone on
	<code>{{.SetupURL}}</code></p>
	<p class="dim">Back to the room in <span class="count" id="count">8</span></p>
</main>
<script>
	const target = {{.TargetJS}};
	const count = document.getElementById('count');
	let left = 8;
	let checking = false;
	setInterval(() => {
		if (checking) return;
		left--;
		count.textContent = Math.max(left, 0);
		if (left <= 0) { checking = true; check(); }
	}, 1000);
	async function check() {
		try {
			const r = await fetch('/status', { cache: 'no-store' });
			const s = await r.json();
			if (s.reachable && s.target) { location.replace(s.target); return; }
		} catch (e) { /* boxd hiccup: try again */ }
		checking = false;
		left = 5;
	}
</script>
</body>
</html>`))
	t.Execute(b, struct {
		Name     string
		Version  string
		Server   string
		SetupURL string
		TargetJS template.JS
		IPs      []string
		Network  string
	}{d.Name, d.Version, d.Server, "http://" + hostname() + ".local", template.JS(targetJS), d.IPs, d.Network})
	return b.String()
}

type storageScreenData struct {
	Name string
	IPs  []string
}

// storageScreenHTML is the "the SD card's data partition is gone" screen
// (MAD-805): shown instead of setup or start when /data never mounted.
// There is nothing to retry on the TV — the fix is at the card — so the
// page just names it. Pairing-wise nothing is lost that wasn't already:
// the box re-pairs from scratch on the next boot with healthy storage.
func storageScreenHTML(d storageScreenData) string {
	b := &strings.Builder{}
	t := template.Must(template.New("storage").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Syncphony Box — storage lost</title>
<style>` + pageStyle + `</style>
</head>
<body>
<main>
	<h1>{{.Name}} lost its storage</h1>
	<p>The card's data partition couldn't be read, so this box forgot its room
	and its pairing. It is still running, but nothing survives a reboot.</p>
	<p class="dim">Power it off, check or re-flash the SD card, and power it back
	on — it will offer a fresh pairing code.</p>
	{{if .IPs}}<p class="dim">Reach it at {{range .IPs}}<code>http://{{.}}</code> {{end}}</p>{{end}}
</main>
</body>
</html>`))
	t.Execute(b, d)
	return b.String()
}

func startScreenHTML(d startScreenData) string {
	targetJS, err := json.Marshal(d.Target)
	if err != nil {
		targetJS = []byte(`""`)
	}
	b := &strings.Builder{}
	t := template.Must(template.New("start").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Syncphony Box</title>
<style>` + pageStyle + `
	#dots::after { content: ""; animation: dots 1.5s steps(4) infinite; }
	@keyframes dots { 0% { content: ""; } 25% { content: "."; } 50% { content: ".."; } 75% { content: "..."; } }
	.count { font-variant-numeric: tabular-nums; }
</style>
</head>
<body>
<main>
	<h1>Syncphony Box</h1>
	<p id="connecting">Connecting to <b>{{.Server}}</b><span id="dots"></span></p>
	<div id="offline" hidden>
		<p>Can't reach <b>{{.Server}}</b> — retrying in <span class="count" id="count">5</span></p>
		<p class="dim">This box is at {{range .IPs}}<code>{{.}}</code> {{end}} · network: {{.Network}}</p>
		<p class="dim">It goes back to the room by itself once the server answers.</p>
	</div>
</main>
<script>
	const target = {{.TargetJS}};
	const connecting = document.getElementById('connecting');
	const offline = document.getElementById('offline');
	const count = document.getElementById('count');
	let left = 5;
	setInterval(() => {
		left--;
		if (left <= 0) { left = 5; check(); }
		count.textContent = left;
	}, 1000);
	async function check() {
		try {
			const r = await fetch('/status', { cache: 'no-store' });
			const s = await r.json();
			if (s.reachable && s.target) { location.replace(s.target); return; }
			connecting.hidden = true;
			offline.hidden = false;
		} catch (e) { /* boxd hiccup: keep the current screen */ }
	}
	check();
</script>
</body>
</html>`))
	t.Execute(b, struct {
		Server   string
		TargetJS template.JS
		IPs      []string
		Network  string
	}{d.Server, template.JS(targetJS), d.IPs, d.Network})
	return b.String()
}

func writePage(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(html))
}
