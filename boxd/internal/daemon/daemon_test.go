package daemon

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
)

func newTestDaemon(t *testing.T, txtContent string) (*Daemon, *recorder) {
	t.Helper()
	dir := t.TempDir()
	txt := filepath.Join(dir, "syncphony.txt")
	env := filepath.Join(dir, "config.env")
	if txtContent != "" {
		os.WriteFile(txt, []byte(txtContent), 0o644)
	}
	rec := &recorder{}
	t.Setenv("SB_TXT", txt)
	t.Setenv("SB_ENV", env)
	// No dev machine has /data mounted: the storage hook pins "healthy" so
	// the screens under test don't trip over the host's mount table.
	t.Setenv("SB_STORAGE", "ok")
	d := New("test-version", log.New(log.Writer(), "", 0), rec.run)
	return d, rec
}

type recorder struct {
	mu     sync.Mutex
	called []string
}

func (r *recorder) run(name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, name+" "+strings.Join(args, " "))
	return nil
}

func (r *recorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.called...)
}

func get(t *testing.T, h http.Handler, target string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "127.0.0.1:8099" // the bridge only answers as loopback
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, target, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8099"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --- bridge: info, events, heartbeat ---------------------------------------

func TestInfo(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\nname=Den TV\n")
	rec := get(t, d.bridgeMux(), "/v1/info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var info struct {
		Version      string   `json:"version"`
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
	}
	json.Unmarshal(rec.Body.Bytes(), &info)
	if info.Version != "test-version" || info.Name != "Den TV" {
		t.Fatalf("got %+v", info)
	}
	if len(info.Capabilities) == 0 {
		t.Fatal("no capabilities")
	}
}

func TestInfoCapabilityCECOnlyWhenUp(t *testing.T) {
	hasCEC := func(t *testing.T, d *Daemon) bool {
		t.Helper()
		var info struct {
			Capabilities []string `json:"capabilities"`
		}
		rec := get(t, d.bridgeMux(), "/v1/info")
		json.Unmarshal(rec.Body.Bytes(), &info)
		for _, c := range info.Capabilities {
			if c == "cec" {
				return true
			}
		}
		return false
	}
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	if hasCEC(t, d) {
		t.Fatal("cec must not be claimed before the adapters registered")
	}
	d.mu.Lock()
	d.cecUp = true
	d.mu.Unlock()
	if !hasCEC(t, d) {
		t.Fatal("cec should be a capability once the adapters are up")
	}
}

func TestEventsAcceptedAndRejected(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	mux := d.bridgeMux()
	if rec := post(t, mux, "/v1/events", `{"type":"playing","roomId":"r1"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("playing: %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, mux, "/v1/events", `{"type":"nonsense"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("nonsense: %d", rec.Code)
	}
	if rec := post(t, mux, "/v1/events", `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
}

func TestHeartbeatRecordsBeat(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	if rec := post(t, d.bridgeMux(), "/v1/heartbeat", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	d.mu.Lock()
	beat := d.lastBeat
	d.mu.Unlock()
	if beat.IsZero() {
		t.Fatal("heartbeat not recorded")
	}
}

// countingSyncs swaps the daemon's disk sync for a counter and returns the
// count function.
func countingSyncs(d *Daemon) func() int {
	var mu sync.Mutex
	n := 0
	d.mu.Lock()
	d.syncDisk = func() error {
		mu.Lock()
		n++
		mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

func TestPairedAndUnpairedEventsFlushDisks(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	syncs := countingSyncs(d)
	for _, ev := range []string{`{"type":"paired","roomId":"r1"}`, `{"type":"unpaired"}`} {
		if rec := post(t, d.bridgeMux(), "/v1/events", ev); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", ev, rec.Code, rec.Body)
		}
		waitFor(t, func() bool { return syncs() >= 1 })
	}
}

func TestPlayingFlushesOncePerStartAndRateLimited(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	syncs := countingSyncs(d)

	// A fresh playing start flushes.
	if rec := post(t, d.bridgeMux(), "/v1/events", `{"type":"playing","roomId":"r1"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("playing: %d", rec.Code)
	}
	waitFor(t, func() bool { return syncs() >= 1 })

	// A flush moments ago closes the rate-limit window: another playback
	// start must not flush again.
	d.mu.Lock()
	d.lastFlush = time.Now()
	d.mu.Unlock()
	before := syncs()
	for _, ev := range []string{`{"type":"paused"}`, `{"type":"idle"}`, `{"type":"playing"}`} {
		if rec := post(t, d.bridgeMux(), "/v1/events", ev); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", ev, rec.Code)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if got := syncs(); got != before {
		t.Fatalf("rate limit failed: %d extra syncs", got-before)
	}
}

// countingWakes swaps the daemon's CEC wake for a counter (MAD-807): the
// TV wake must fire once per playback start — not per repeat of the
// playing event — and never for other events.
func countingWakes(d *Daemon) func() int {
	var mu sync.Mutex
	n := 0
	d.mu.Lock()
	d.wakeTV = func() {
		mu.Lock()
		n++
		mu.Unlock()
	}
	d.mu.Unlock()
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

func TestPlayingWakesTheTVOncePerStart(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	wakes := countingWakes(d)
	mux := d.bridgeMux()

	post(t, mux, "/v1/events", `{"type":"playing","roomId":"r1"}`)
	waitFor(t, func() bool { return wakes() == 1 })

	// A repeat of playing is not a new start.
	time.Sleep(20 * time.Millisecond)
	post(t, mux, "/v1/events", `{"type":"playing","roomId":"r1"}`)
	time.Sleep(50 * time.Millisecond)
	if got := wakes(); got != 1 {
		t.Fatalf("repeat playing woke %d times", got)
	}

	// Pause, then play again: a new start.
	post(t, mux, "/v1/events", `{"type":"paused"}`)
	post(t, mux, "/v1/events", `{"type":"playing","roomId":"r1"}`)
	waitFor(t, func() bool { return wakes() == 2 })

	// Other events never wake.
	post(t, mux, "/v1/events", `{"type":"idle"}`)
	post(t, mux, "/v1/events", `{"type":"paired"}`)
	time.Sleep(50 * time.Millisecond)
	if got := wakes(); got != 2 {
		t.Fatalf("wakes: %d", got)
	}
}

// --- bridge: origin, host, CORS/PNA -----------------------------------------

func TestOriginAllowedOnlyForConfiguredServer(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	mux := d.bridgeMux()

	rec := get(t, mux, "/v1/info", "Origin", "https://s.example")
	if rec.Code != http.StatusOK {
		t.Fatalf("server origin refused: %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://s.example" {
		t.Fatalf("ACAO = %q", got)
	}

	for _, origin := range []string{"https://evil.example", "http://s.example", "https://s.example:8443", "null"} {
		rec := get(t, mux, "/v1/info", "Origin", origin)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("origin %q not refused: %d", origin, rec.Code)
		}
	}

	// No Origin (curl, same-origin): allowed.
	if rec := get(t, mux, "/v1/info"); rec.Code != http.StatusOK {
		t.Fatalf("no-origin refused: %d", rec.Code)
	}
}

func TestUnconfiguredBridgeRefusesEveryOrigin(t *testing.T) {
	d, _ := newTestDaemon(t, "")
	rec := get(t, d.bridgeMux(), "/v1/info", "Origin", "https://any.example")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestPreflightAnswersPrivateNetworkAccess(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	req := httptest.NewRequest(http.MethodOptions, "/v1/events", nil)
	req.Host = "127.0.0.1:8099"
	req.Header.Set("Origin", "https://s.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()
	d.bridgeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("ACAPN = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://s.example" {
		t.Fatalf("ACAO = %q", got)
	}

	// A preflight from anywhere else is refused.
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	d.bridgeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("evil preflight: %d", rec.Code)
	}
}

func TestReboundHostRefused(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Host = "evil.example:8099" // a DNS-rebound name resolving to loopback
	rec := httptest.NewRecorder()
	d.bridgeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestRebootAndReloadRunSystemctl(t *testing.T) {
	d, rec := newTestDaemon(t, "server_url=https://s.example\n")
	mux := d.bridgeMux()
	if r := post(t, mux, "/v1/reboot", ""); r.Code != http.StatusAccepted {
		t.Fatalf("reboot: %d", r.Code)
	}
	if r := post(t, mux, "/v1/reload", ""); r.Code != http.StatusAccepted {
		t.Fatalf("reload: %d", r.Code)
	}
	waitFor(t, func() bool {
		return strings.Contains(strings.Join(rec.calls(), ";"), "systemctl reboot") &&
			strings.Contains(strings.Join(rec.calls(), ";"), "systemctl restart syncphony-kiosk.service")
	})
}

// --- screens ----------------------------------------------------------------

// TestRemoteLongPressOpensSetupAndShowsInfo: the box's one remote gesture
// (MAD-807). A 3-second OK hold opens the LAN setup window and puts the
// box's IP and version on the TV via the info screen on /start.
func TestRemoteLongPressOpensSetupAndShowsInfo(t *testing.T) {
	d, rec := newTestDaemon(t, "server_url=https://s.example\nname=Den TV\n")
	d.onRemoteLongPress()

	if !d.setupWindowOpen() {
		t.Fatal("setup window not open after the long press")
	}
	waitFor(t, func() bool {
		return strings.Contains(strings.Join(rec.calls(), ";"), "systemctl restart syncphony-kiosk.service")
	})

	body := get(t, d.bridgeMux(), "/start").Body.String()
	for _, want := range []string{"Den TV", "test-version"} {
		if !strings.Contains(body, want) {
			t.Errorf("info screen should show %q:\n%s", want, body)
		}
	}
}

func TestInfoScreenYieldsBackToStart(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	d.mu.Lock()
	d.infoUntil = time.Now().Add(-time.Second)
	d.mu.Unlock()
	body := get(t, d.bridgeMux(), "/start").Body.String()
	if !strings.Contains(body, "Connecting to") {
		t.Fatalf("the ordinary start screen should be back: %s", body[:min(200, len(body))])
	}
}

func TestStartScreenRedirectsToSetupWhenUnconfigured(t *testing.T) {
	d, _ := newTestDaemon(t, "")
	rec := get(t, d.bridgeMux(), "/start")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("got %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSetupScreenHasQRAndLocalAddress(t *testing.T) {
	d, _ := newTestDaemon(t, "")
	rec := get(t, d.bridgeMux(), "/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Error("no QR image")
	}
	if !strings.Contains(body, ".local") {
		t.Error("no .local address")
	}
	if !strings.Contains(body, "Set up this box") {
		t.Error("wrong page")
	}
}

func TestSetupScreenRedirectsToStartWhenConfigured(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	rec := get(t, d.bridgeMux(), "/setup")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/start" {
		t.Fatalf("got %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestStatusReportsServerAndTarget(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\nname=Den TV\n")
	// A server that answers /healthz.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	os.WriteFile(config.PathsFromEnv().Txt, []byte("server_url="+srv.URL+"\nname=Den TV\n"), 0o644)
	d.reloadConfig()

	rec := get(t, d.bridgeMux(), "/status")
	var st struct {
		Configured bool   `json:"configured"`
		Server     string `json:"server"`
		Reachable  bool   `json:"reachable"`
		Target     string `json:"target"`
		Storage    bool   `json:"storage"`
	}
	json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Configured || st.Server != srv.URL || !st.Reachable {
		t.Fatalf("got %+v", st)
	}
	if !st.Storage {
		t.Fatalf("storage hook is ok but /status reports %+v", st)
	}
	if want := config.TargetURL(srv.URL, "test-version", "Den TV"); st.Target != want {
		t.Fatalf("target %q, want %q", st.Target, want)
	}
}

// --- storage-lost screen (MAD-805) -------------------------------------------

// TestStorageScreenTakesOverStartAndSetup: without /data the box still
// boots, but the kiosk must not pretend — the storage screen wins over
// both entry pages, whatever the config says.
func TestStorageScreenTakesOverStartAndSetup(t *testing.T) {
	for _, tc := range []struct {
		name, txt, path string
	}{
		{"unconfigured setup", "", "/setup"},
		{"unconfigured start", "", "/start"},
		{"configured start", "server_url=https://s.example\n", "/start"},
		{"configured setup", "server_url=https://s.example\n", "/setup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDaemon(t, tc.txt)
			t.Setenv("SB_STORAGE", "missing") // helper pins ok; lose /data here
			rec := get(t, d.bridgeMux(), tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "lost its storage") {
				t.Fatalf("no storage screen: %s", rec.Body)
			}
		})
	}
}

func TestStorageScreenNamesWhatToDo(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\nname=Den TV\n")
	t.Setenv("SB_STORAGE", "missing")
	rec := get(t, d.bridgeMux(), "/storage")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{"Den TV", "SD card", "pairing"} {
		if !strings.Contains(body, want) {
			t.Errorf("storage screen should mention %q", want)
		}
	}
}

// --- LAN setup form ----------------------------------------------------------

func TestLANClosedWhenConfigured(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	if rec := get(t, d.lanMux(), "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestLANOpenWhenUnconfigured(t *testing.T) {
	d, _ := newTestDaemon(t, "")
	rec := get(t, d.lanMux(), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Your Syncphony server") {
		t.Error("form missing")
	}
}

func TestSetupSaveFullFlow(t *testing.T) {
	d, rec := newTestDaemon(t, "")
	// The "Syncphony server": /healthz must answer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	form := url.Values{"server_url": {srv.URL}, "name": {"Den TV"}}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rp := httptest.NewRecorder()
	d.lanMux().ServeHTTP(rp, req)
	if rp.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rp.Code, rp.Body)
	}
	if !strings.Contains(rp.Body.String(), "Saved") {
		t.Fatalf("no success page: %s", rp.Body)
	}

	// syncphony.txt is the source of truth and kept the comments flow.
	c := config.ParseFile(config.PathsFromEnv().Txt)
	if c.ServerURL != srv.URL || c.Name != "Den TV" || c.Hostname != "den-tv" {
		t.Fatalf("saved config: %+v", c)
	}
	// config.env regenerated for the kiosk.
	if _, err := os.Stat(config.PathsFromEnv().Env); err != nil {
		t.Fatalf("config.env: %v", err)
	}
	// The kiosk was restarted; the setup window closed.
	waitFor(t, func() bool {
		return strings.Contains(strings.Join(rec.calls(), ";"), "systemctl restart syncphony-kiosk.service")
	})
	if d.setupWindowOpen() {
		t.Fatal("setup window still open after save")
	}
}

func TestSetupSaveRejectsUnreachableServer(t *testing.T) {
	d, rec := newTestDaemon(t, "")
	// Nothing listens on this port.
	form := url.Values{"server_url": {"http://127.0.0.1:1"}}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rp := httptest.NewRecorder()
	d.lanMux().ServeHTTP(rp, req)
	if rp.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rp.Code)
	}
	if !strings.Contains(rp.Body.String(), "127.0.0.1:1") {
		t.Fatalf("no error shown: %s", rp.Body)
	}
	// Nothing was saved and nothing was restarted.
	if c := config.ParseFile(config.PathsFromEnv().Txt); c.ServerURL != "" {
		t.Fatalf("saved anyway: %+v", c)
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("commands run: %v", calls)
	}
}

func TestSetupSaveRejectsNonHTTPURL(t *testing.T) {
	d, _ := newTestDaemon(t, "")
	for _, bad := range []string{"", "not a url", "ftp://x.example", "javascript:alert(1)"} {
		form := url.Values{"server_url": {bad}}
		req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rp := httptest.NewRecorder()
		d.lanMux().ServeHTTP(rp, req)
		if rp.Code != http.StatusBadRequest {
			t.Fatalf("%q: status %d", bad, rp.Code)
		}
	}
}

// --- misc --------------------------------------------------------------------

func TestServerOrigin(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example:8443\n")
	if got := d.serverOrigin(); got != "https://s.example:8443" {
		t.Fatalf("got %q", got)
	}
	d2, _ := newTestDaemon(t, "server_url=http://s.example/\n")
	if got := d2.serverOrigin(); got != "http://s.example" {
		t.Fatalf("got %q", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
