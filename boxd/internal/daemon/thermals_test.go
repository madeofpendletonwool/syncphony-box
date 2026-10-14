package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

func TestParseTempC(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want float64
		ok   bool
	}{
		{"temp=52.3'C\n", 52.3, true},
		{"temp=61.9'C", 61.9, true},
		{"temp=0.0'C", 0, true},
		{"garbage", 0, false},
		{"", 0, false},
	} {
		got, ok := parseTempC(tc.out)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseTempC(%q) = %v,%v want %v,%v", tc.out, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseThrottleFlags(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want uint64
	}{
		{"throttled=0x0\n", 0},
		{"throttled=0x50001", 0x50001},
		{"throttled=0x4", 0x4},
		{"throttled=0", 0},
		{"garbage", 0},
	} {
		if got := parseThrottleFlags(tc.out); got != tc.want {
			t.Errorf("parseThrottleFlags(%q) = %#x want %#x", tc.out, got, tc.want)
		}
	}
}

func TestThermalsDecodesFlags(t *testing.T) {
	// 0x50001: under-voltage now (bit 0), under-voltage occurred (bit 16),
	// throttling occurred (bit 18) — nothing limiting *right now*.
	th := thermals{at: time.Now(), rawFlags: 0x50001}
	if !th.underVoltage() || !th.throttleOccurred() || th.limitedNow() {
		t.Fatalf("0x50001 decoded wrong: %+v", th.infoFields())
	}
	// 0x4: throttled right now.
	th.rawFlags = 0x4
	if !th.limitedNow() || th.underVoltage() || th.throttleOccurred() {
		t.Fatalf("0x4 decoded wrong: %+v", th.infoFields())
	}
	// 0x10000: under-voltage has happened, is not happening.
	th.rawFlags = 0x10000
	if th.limitedNow() || th.underVoltage() || !th.throttleOccurred() {
		t.Fatalf("0x10000 decoded wrong: %+v", th.infoFields())
	}
}

// stubVcgencmd answers like the firmware tool; the daemon field swap is
// what tests use instead of a Pi.
func stubVcgencmd(temp string, flags string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		if len(args) == 0 {
			return "", errors.New("no args")
		}
		switch args[0] {
		case "measure_temp":
			return temp, nil
		case "get_throttled":
			return flags, nil
		}
		return "", errors.New("unknown: " + args[0])
	}
}

func TestSampleThermalsStoresAndReports(t *testing.T) {
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	d.vcgencmd = stubVcgencmd("temp=48.5'C\n", "throttled=0x0\n")
	if !d.sampleThermals() {
		t.Fatal("sampling reported vcgencmd missing")
	}

	for _, target := range []string{"/v1/info", "/status"} {
		rec := get(t, d.bridgeMux(), target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		if m["temperatureC"] != 48.5 {
			t.Fatalf("%s: temperatureC = %v", target, m["temperatureC"])
		}
		if m["throttled"] != false {
			t.Fatalf("%s: throttled = %v", target, m["throttled"])
		}
	}

	// A throttled sample shows up as such.
	d.vcgencmd = stubVcgencmd("temp=85.0'C\n", "throttled=0x40004\n")
	d.sampleThermals()
	rec := get(t, d.bridgeMux(), "/v1/info")
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	if m["throttled"] != true || m["underVoltage"] != false {
		t.Fatalf("throttled sample: %v", m)
	}
}

func TestThermalsAbsentWithoutVcgencmd(t *testing.T) {
	// Fresh daemon, no sample yet: /v1/info keeps its ADR 0016 shape.
	d, _ := newTestDaemon(t, "server_url=https://s.example\n")
	rec := get(t, d.bridgeMux(), "/v1/info")
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	for _, key := range []string{"temperatureC", "throttled", "underVoltage", "throttleOccurred"} {
		if _, ok := m[key]; ok {
			t.Fatalf("unsampled daemon reports %q", key)
		}
	}
	if m["version"] != "test-version" {
		t.Fatalf("info lost its core: %v", m)
	}

	// And where vcgencmd doesn't exist (not a Pi), the loop stops and
	// nothing is ever reported.
	d2, _ := newTestDaemon(t, "server_url=https://s.example\n")
	d2.vcgencmd = func(args ...string) (string, error) {
		return "", exec.ErrNotFound
	}
	d2.sampleThermalsLoop() // returns immediately
	rec = get(t, d2.bridgeMux(), "/v1/info")
	m = nil
	json.Unmarshal(rec.Body.Bytes(), &m)
	if _, ok := m["temperatureC"]; ok {
		t.Fatal("no-vcgencmd daemon must not report thermals")
	}
}
