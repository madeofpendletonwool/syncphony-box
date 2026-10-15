package daemon

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The nightly update (MAD-810) asks /status whether the room is playing
// before it starts and before it reboots: the last event must say so and
// the page must still be beating.
func TestStatusPlayingNeedsALivePage(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	mux := d.bridgeMux()
	playing := func() bool {
		t.Helper()
		var st struct {
			Playing bool `json:"playing"`
		}
		if err := json.Unmarshal(get(t, mux, "/status").Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st.Playing
	}
	if playing() {
		t.Fatal("a fresh box reports playing")
	}
	post(t, mux, "/v1/heartbeat", "")
	post(t, mux, "/v1/events", `{"type":"playing"}`)
	if !playing() {
		t.Fatal("playing with a live page not reported")
	}
	d.mu.Lock()
	d.lastBeat = time.Now().Add(-2 * heartbeatWindow)
	d.mu.Unlock()
	if playing() {
		t.Fatal("a dead page's last event still counts as playing")
	}
	post(t, mux, "/v1/heartbeat", "")
	post(t, mux, "/v1/events", `{"type":"paused"}`)
	if playing() {
		t.Fatal("paused reported as playing")
	}
}

// /v1/info carries the last update for the Screens list (MAD-811), and
// leaves the field out until the box has run one.
func TestInfoReportsTheLastUpdate(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	info := func() map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(get(t, d.bridgeMux(), "/v1/info").Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if _, ok := info()["update"]; ok {
		t.Fatal("update reported before any run")
	}
	os.WriteFile(os.Getenv("SB_UPDATE_STATE"), []byte(`{"at":"2026-10-16T04:03:00Z","result":"failed","error":"apt-get update: exit status 100","version":"1.3.0"}`), 0o644)
	u, ok := info()["update"].(map[string]any)
	if !ok {
		t.Fatalf("no update in %v", info())
	}
	if u["at"] != "2026-10-16T04:03:00Z" || u["result"] != "failed" || u["error"] != "apt-get update: exit status 100" {
		t.Fatalf("update %v", u)
	}
}
