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
	}
	json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Configured || st.Server != srv.URL || !st.Reachable {
		t.Fatalf("got %+v", st)
	}
	if want := config.TargetURL(srv.URL, "test-version", "Den TV"); st.Target != want {
		t.Fatalf("target %q, want %q", st.Target, want)
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
