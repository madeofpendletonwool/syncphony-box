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
	.notice {
		background: #2a1518; border: 1px solid #5c2a30; border-radius: 1rem;
		color: #f1b8ba; padding: 1rem 1.5rem;
	}
	.steps { text-align: left; display: inline-block; }
`

// controlStyle is for the screens with buttons (the Wi-Fi controls): a
// pointer when there is a mouse, and a focus ring big enough to follow
// from the couch when the remote's arrows or a keyboard move it.
var controlStyle = `
	body { cursor: auto; }
	.actions { display: flex; gap: 1rem; justify-content: center; flex-wrap: wrap; margin-top: 1.5rem; }
	button, .button {
		font: inherit; font-size: 1.3rem; font-weight: 600;
		padding: 0.9rem 1.6rem; border-radius: 0.8rem; border: 0;
		color: oklch(0.98 0.01 295); background: oklch(0.45 0.112 295);
		text-decoration: none; cursor: pointer;
	}
	button.secondary, .button.secondary { background: #2c2c34; color: #f4f4f5; }
	button:disabled { opacity: 0.5; cursor: default; }
	button:focus-visible, .button:focus-visible, input:focus-visible {
		outline: 4px solid oklch(0.75 0.16 295); outline-offset: 3px;
	}
	.count { font-variant-numeric: tabular-nums; }
`

// arrowFocusJS lets a TV remote (whose arrows arrive as arrow keys, see
// the cec package) walk the buttons: Up/Left and Down/Right move the
// focus, Enter clicks. Typing fields keep their own Left/Right.
const arrowFocusJS = `
	document.addEventListener('keydown', (e) => {
		const keys = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 };
		if (!(e.key in keys)) return;
		const typing = document.activeElement && document.activeElement.tagName === 'INPUT';
		if (typing && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) return;
		const all = [...document.querySelectorAll('button, a.button, input')]
			.filter((el) => !el.disabled && el.offsetParent !== null);
		if (!all.length) return;
		const i = all.indexOf(document.activeElement);
		const next = all[(i < 0 ? 0 : i + keys[e.key] + all.length) % all.length];
		next.focus();
		e.preventDefault();
	});
`

type setupScreenData struct {
	QR     string
	URL    string
	IPs    []string
	Notice string // after a hotspot join that couldn't save the server URL
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
<style>` + pageStyle + controlStyle + `</style>
</head>
<body>
<main>
	<h1>Set up this box</h1>
	<p>On your phone, joined to this network, scan the code —</p>
	<div class="qr">{{if .QR}}<img src="{{.QR}}" alt="QR code">{{end}}</div>
	<p>or open <b>{{.URL}}</b></p>
	{{if .IPs}}<p class="dim">or try {{range .IPs}}<code>{{.}}</code> {{end}}</p>{{end}}
	<p class="dim">There you can set which Syncphony server this box shows.</p>
	{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
	<div id="wifi" hidden>
		<p id="wifi-msg"></p>
		<p id="wifi-err" class="notice" hidden></p>
		<div class="actions">
			<button id="ap-now" hidden>Start setup Wi-Fi now</button>
			<a class="button secondary" id="pick" href="/wifi" hidden>Choose Wi-Fi on this screen</a>
		</div>
	</div>
</main>
<script>
	// The TV follows the box: to the Wi-Fi setup hotspot when the box has
	// no network (MAD-808), to the start page once a server is set. Until
	// the hotspot starts, it says when it will (or why it can't), and
	// offers to start it now or to pick the Wi-Fi right here.
	const box = document.getElementById('wifi');
	const msg = document.getElementById('wifi-msg');
	const err = document.getElementById('wifi-err');
	const apNow = document.getElementById('ap-now');
	const pick = document.getElementById('pick');
	let deadline = null, starting = false, w = {}, net = '';

	function render() {
		box.hidden = false;
		const offline = w.hotspotIn != null;
		if (!w.device) {
			if (net === 'online') { box.hidden = true; return; }
			msg.textContent = 'No network, and no Wi-Fi this box can use' +
				(w.unavailable ? ' (' + w.unavailable + ')' : '') + '. Plug in an Ethernet cable to set it up.';
		} else if (starting || (offline && deadline !== null && deadline <= Date.now())) {
			msg.textContent = 'Starting the setup Wi-Fi…';
		} else if (offline) {
			const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
			const m = Math.floor(left / 60), sec = String(left % 60).padStart(2, '0');
			msg.innerHTML = 'No network yet. This box starts its own setup Wi-Fi in <b class="count">' +
				m + ':' + sec + '</b> — or start it now.';
		} else {
			msg.textContent = '';
		}
		err.hidden = !w.startError;
		err.textContent = w.startError ? "Couldn't start the setup Wi-Fi: " + w.startError : '';
		apNow.hidden = !w.device || !offline;
		apNow.disabled = starting;
		pick.hidden = !w.device;
	}

	async function poll() {
		try {
			const s = await (await fetch('/status', { cache: 'no-store' })).json();
			if (s.wifiSetup) { location.replace('/hotspot'); return; }
			if (s.configured) { location.replace('/start'); return; }
			w = s.wifi || {};
			net = s.network;
			deadline = w.hotspotIn != null ? Date.now() + w.hotspotIn * 1000 : null;
			if (w.startError) starting = false;
			render();
		} catch (e) { /* boxd hiccup: stay */ }
	}

	apNow.addEventListener('click', async () => {
		starting = true;
		render();
		try {
			const r = await fetch('/wifi/hotspot', { method: 'POST' });
			if (!r.ok) {
				starting = false;
				w.startError = (await r.json()).error;
				render();
			}
		} catch (e) { starting = false; render(); }
	});
` + arrowFocusJS + `
	poll();
	setInterval(poll, 3000);
	setInterval(render, 1000);
</script>
</body>
</html>`))
	t.Execute(b, struct {
		QR     template.URL // boxd generates this data URI itself; html/template would otherwise rewrite it
		URL    string
		IPs    []string
		Notice string
	}{template.URL(d.QR), d.URL, ips, d.Notice})
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
			if (s.wifiSetup) { location.replace('/hotspot'); return; }
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

type hotspotScreenData struct {
	SSID     string
	QR       string
	Phase    string
	Joining  string
	Error    string
	Address  string
	Checking bool
}

// hotspotScreenHTML is the TV during Wi-Fi setup (MAD-808): join the
// box's own network from the QR code, then the setup page opens on the
// phone. It follows a join live ("Joining Home…", then either the room
// or the hotspot again with the reason) by polling /status.
func hotspotScreenHTML(d hotspotScreenData) string {
	b := &strings.Builder{}
	t := template.Must(template.New("hotspot").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Syncphony Box — Wi-Fi setup</title>
<style>` + pageStyle + controlStyle + `
	.qr { width: 22rem; }
	#dots::after { content: ""; animation: dots 1.5s steps(4) infinite; }
	@keyframes dots { 0% { content: ""; } 25% { content: "."; } 50% { content: ".."; } 75% { content: "..."; } }
</style>
</head>
<body>
<main>
{{if .Joining}}
	<h1>Joining {{.Joining}}<span id="dots"></span></h1>
	<p class="dim">Your phone has left this box's setup network. If the box can't
	join, the setup network comes back here with the reason.</p>
{{else if .Checking}}
	<h1>Looking for a known network<span id="dots"></span></h1>
	<p class="dim">The setup network is back in a moment if none turns up.</p>
{{else}}
	<h1>Connect this box to Wi-Fi</h1>
	{{if .Error}}<p class="notice">{{.Error}}</p>{{end}}
	<div class="qr">{{if .QR}}<img src="{{.QR}}" alt="QR code">{{end}}</div>
	<div class="steps">
		<p>1. Scan to join <b>{{.SSID}}</b> (no password)</p>
		<p>2. A setup page opens — pick your Wi-Fi</p>
	</div>
	<p class="dim">If the page doesn't open, go to <code>{{.Address}}</code></p>
	<div class="actions">
		<a class="button secondary" href="/wifi">Choose Wi-Fi on this screen instead</a>
	</div>
{{end}}
</main>
<script>` + arrowFocusJS + `
	const shown = {{.State}};
	setInterval(async () => {
		try {
			const s = await (await fetch('/status', { cache: 'no-store' })).json();
			if (!s.wifiSetup) { location.replace('/start'); return; }
			const w = s.wifi || {};
			if ([w.phase, w.joining, w.error].join('|') !== shown) location.reload();
		} catch (e) { /* boxd hiccup: stay */ }
	}, 2000);
</script>
</body>
</html>`))
	state := d.Phase + "|" + d.Joining + "|" + d.Error
	stateJS, err := json.Marshal(state)
	if err != nil {
		stateJS = []byte(`""`)
	}
	t.Execute(b, struct {
		SSID     string
		QR       template.URL // generated by boxd, see setupScreenHTML
		Joining  string
		Error    string
		Address  string
		Checking bool
		State    template.JS
	}{d.SSID, template.URL(d.QR), d.Joining, d.Error, d.Address, d.Checking, template.JS(stateJS)})
	return b.String()
}

// wifiScreenHTML is the TV's own network picker: for a box with a
// keyboard (or a remote, for the open networks and the buttons), the
// house Wi-Fi without a phone. The list comes from a fresh scan, which
// takes the radio from the hotspot if it is up; while the screen is in
// use it holds the hotspot off (/wifi/hold), and after three idle
// minutes it hands the TV back to the setup screen.
func wifiScreenHTML() string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Syncphony Box — Wi-Fi</title>
<style>` + pageStyle + controlStyle + `
	main { width: min(46rem, 90vw); }
	#list { display: flex; flex-direction: column; gap: 0.6rem; margin: 1.5rem 0; max-height: 50vh; overflow-y: auto; padding: 6px; }
	#list button {
		display: flex; justify-content: space-between; align-items: center;
		background: #1c1c22; color: #f4f4f5; text-align: left;
	}
	#list .meta { color: #8b8b94; font-weight: 400; font-size: 1.1rem; white-space: nowrap; margin-left: 1rem; }
	form { text-align: left; margin: 1.5rem 0; }
	label { display: block; font-size: 1.3rem; margin: 1rem 0 0.4rem; }
	input[type=text], input[type=password] {
		width: 100%; box-sizing: border-box; font: inherit; font-size: 1.4rem;
		padding: 0.8rem 1rem; background: #1c1c22; color: #f4f4f5;
		border: 1px solid #2c2c34; border-radius: 0.8rem;
	}
	.show { display: flex; gap: 0.6rem; align-items: center; color: #8b8b94; font-size: 1.1rem; }
	#dots::after { content: ""; animation: dots 1.5s steps(4) infinite; }
	@keyframes dots { 0% { content: ""; } 25% { content: "."; } 50% { content: ".."; } 75% { content: "..."; } }
</style>
</head>
<body>
<main>
	<h1 id="title">Choose this box's Wi-Fi</h1>
	<p id="msg" class="dim"></p>
	<p id="err" class="notice" hidden></p>

	<div id="pickView">
		<div id="list"></div>
		<div class="actions">
			<button class="secondary" id="other">Network not listed…</button>
			<button class="secondary" id="rescan">Look again</button>
			<a class="button secondary" href="/setup">Back</a>
		</div>
	</div>

	<form id="joinView" hidden>
		<div id="ssidRow">
			<label for="ssid">Network name</label>
			<input id="ssid" type="text" autocomplete="off" spellcheck="false">
		</div>
		<div id="pwRow">
			<label for="password" id="pwLabel">Password</label>
			<input id="password" type="password" autocomplete="off" spellcheck="false">
			<label class="show"><input type="checkbox" id="showpw"> Show password</label>
		</div>
		<div class="actions">
			<button type="submit" id="connect">Connect</button>
			<button type="button" class="secondary" id="cancel">Back to the list</button>
		</div>
	</form>
</main>
<script>
	const $ = (id) => document.getElementById(id);
	const msg = $('msg'), err = $('err'), list = $('list');
	let chosen = null; // { ssid, secure, hidden }
	let joining = '';
	let lastInput = Date.now();
	const IDLE_MS = 3 * 60 * 1000;

	function say(text) { msg.textContent = text; }
	function fail(text) { err.textContent = text || ''; err.hidden = !text; }

	function bars(signal) {
		return signal >= 70 ? '▂▄▆█' : signal >= 45 ? '▂▄▆' : signal >= 20 ? '▂▄' : '▂';
	}

	function showPick() {
		chosen = null;
		$('title').textContent = "Choose this box's Wi-Fi";
		$('pickView').hidden = false;
		$('joinView').hidden = true;
		const first = list.querySelector('button') || $('other');
		first.focus();
	}

	function showJoin(net) {
		chosen = net;
		$('pickView').hidden = true;
		$('joinView').hidden = false;
		$('ssidRow').hidden = !net.hidden;
		$('pwRow').hidden = !net.secure && !net.hidden;
		$('pwLabel').textContent = net.hidden ? 'Password (leave empty for an open network)' : 'Password for ' + net.ssid;
		$('password').value = '';
		$('title').textContent = net.hidden ? 'A network that isn’t listed' : net.ssid;
		(net.hidden ? $('ssid') : (net.secure ? $('password') : $('connect'))).focus();
	}

	async function scan() {
		fail('');
		say('Looking for networks…');
		list.replaceChildren();
		$('rescan').disabled = true;
		try {
			const r = await fetch('/wifi/scan', { method: 'POST' });
			const body = await r.json();
			if (!r.ok) throw new Error(body.error || 'Couldn’t look for networks.');
			for (const n of body.networks) {
				const b = document.createElement('button');
				const name = document.createElement('span');
				name.textContent = n.ssid;
				const meta = document.createElement('span');
				meta.className = 'meta';
				meta.textContent = (n.secure ? '🔒 ' : '') + bars(n.signal);
				b.append(name, meta);
				b.addEventListener('click', () => showJoin({ ssid: n.ssid, secure: n.secure, hidden: false }));
				list.append(b);
			}
			say(body.networks.length ? 'Pick your network.' : 'No networks found. Look again, or type its name.');
		} catch (e) {
			say('');
			fail(e.message);
		}
		$('rescan').disabled = false;
		showPick();
	}

	async function join(e) {
		e.preventDefault();
		fail('');
		const ssid = chosen.hidden ? $('ssid').value.trim() : chosen.ssid;
		const form = new URLSearchParams();
		form.set(chosen.hidden ? 'other_ssid' : 'ssid', ssid);
		form.set('password', $('password').value);
		$('connect').disabled = true;
		try {
			const r = await fetch('/wifi/join', { method: 'POST', body: form });
			const body = await r.json();
			if (!r.ok) throw new Error(body.error);
			joining = ssid;
			$('title').innerHTML = '';
			$('title').append('Joining ' + ssid, Object.assign(document.createElement('span'), { id: 'dots' }));
			$('joinView').hidden = true;
			say('This takes up to a minute.');
			follow();
		} catch (e2) {
			fail(e2.message);
			$('connect').disabled = false;
		}
	}

	async function follow() {
		try {
			const s = await (await fetch('/status', { cache: 'no-store' })).json();
			const w = s.wifi || {};
			if (!w.joining) {
				if (w.error) {
					joining = '';
					$('connect').disabled = false;
					showJoin(chosen);
					fail(w.error);
					say('');
					return;
				}
				location.replace('/start'); // on the network: the room, or the phone setup QR
				return;
			}
		} catch (e) { /* boxd hiccup: keep watching */ }
		setTimeout(follow, 1500);
	}

	$('joinView').addEventListener('submit', join);
	$('cancel').addEventListener('click', () => { fail(''); showPick(); });
	$('other').addEventListener('click', () => showJoin({ ssid: '', secure: true, hidden: true }));
	$('rescan').addEventListener('click', scan);
	$('showpw').addEventListener('change', (e) => { $('password').type = e.target.checked ? 'text' : 'password'; });
	for (const ev of ['keydown', 'pointerdown', 'input']) {
		document.addEventListener(ev, () => { lastInput = Date.now(); }, true);
	}
	document.addEventListener('keydown', (e) => {
		if (e.key !== 'Escape' || joining) return;
		if (!$('joinView').hidden) showPick(); else location.replace('/setup');
	});
	// While someone is here, the hotspot waits; once nobody is, the TV
	// goes back to the setup screen and the usual timing resumes.
	setInterval(() => {
		if (joining) return;
		if (Date.now() - lastInput > IDLE_MS) { location.replace('/setup'); return; }
		fetch('/wifi/hold', { method: 'POST' }).catch(() => {});
	}, 5000);
` + arrowFocusJS + `
	scan();
</script>
</body>
</html>`
}

func writePage(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(html))
}
