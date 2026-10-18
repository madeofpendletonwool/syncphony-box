package wifi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// script is a scripted nmcli: replies by the joined args' prefix, and a
// log of every call.
type script struct {
	calls   []string
	replies map[string]string
	fail    map[string]error
}

func (s *script) nmcli(args ...string) (string, error) {
	call := strings.Join(args, " ")
	s.calls = append(s.calls, call)
	for prefix, err := range s.fail {
		if strings.HasPrefix(call, prefix) {
			return "", err
		}
	}
	for prefix, out := range s.replies {
		if strings.HasPrefix(call, prefix) {
			return out, nil
		}
	}
	return "", nil
}

// No real waits between rescans in tests.
func init() { sleep = func(time.Duration) {} }

func newScripted(s *script) *NM {
	return NewWith(s.nmcli, func(string) ([]byte, error) {
		return []byte("dc:a6:32:12:ab:cd\n"), nil
	})
}

func TestSplitTerse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"wlan0:wifi", []string{"wlan0", "wifi"}},
		{`My\:Net:70:WPA2`, []string{"My:Net", "70", "WPA2"}},
		{`back\\slash::`, []string{`back\slash`, "", ""}},
		{"", nil},
	} {
		if got := splitTerse(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitTerse(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDevice(t *testing.T) {
	s := &script{replies: map[string]string{
		"-t -f DEVICE,TYPE device status": "eth0:ethernet\nlo:loopback\np2p-dev-wlan0:wifi-p2p\nwlan0:wifi\n",
	}}
	dev, err := newScripted(s).Device()
	if err != nil || dev != "wlan0" {
		t.Fatalf("Device() = %q, %v; want wlan0", dev, err)
	}

	s = &script{replies: map[string]string{"-t -f DEVICE,TYPE device status": "eth0:ethernet\n"}}
	if _, err := newScripted(s).Device(); err == nil {
		t.Fatal("Device() with no Wi-Fi: want an error")
	}
}

func TestOnline(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      bool
	}{
		{"nothing", "lo:loopback:activated\n", false},
		{"ethernet", "Wired connection 1:802-3-ethernet:activated\nlo:loopback:activated\n", true},
		{"wifi", "preconfigured:802-11-wireless:activated\n", true},
		{"wifi still activating", "preconfigured:802-11-wireless:activating\n", false},
		{"only the setup AP", APConnection + ":802-11-wireless:activated\n", false},
		{"AP and ethernet", APConnection + ":802-11-wireless:activated\nWired:802-3-ethernet:activated\n", true},
	} {
		if got := parseOnline(tc.out); got != tc.want {
			t.Errorf("%s: parseOnline = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSavedWiFi(t *testing.T) {
	s := &script{replies: map[string]string{"-t -f NAME,TYPE connection show": APConnection + ":802-11-wireless\nWired:802-3-ethernet\n"}}
	if newScripted(s).SavedWiFi() {
		t.Error("the setup AP alone is not a saved network")
	}
	s = &script{replies: map[string]string{"-t -f NAME,TYPE connection show": "Home:802-11-wireless\n"}}
	if !newScripted(s).SavedWiFi() {
		t.Error("Home is a saved network")
	}
}

func TestAPSSID(t *testing.T) {
	n := newScripted(&script{})
	if got := n.APSSID("wlan0"); got != "Syncphony-Box-ABCD" {
		t.Errorf("APSSID = %q, want Syncphony-Box-ABCD", got)
	}
	n = NewWith((&script{}).nmcli, func(string) ([]byte, error) { return nil, errors.New("no") })
	if got := n.APSSID("wlan0"); got != "Syncphony-Box" {
		t.Errorf("APSSID without a MAC = %q, want Syncphony-Box", got)
	}
}

func TestParseScan(t *testing.T) {
	out := strings.Join([]string{
		"Home:54:WPA2",
		"Home:81:WPA2", // the same network from a second AP: keep the stronger
		"Cafe:60:",
		"Old:30:--",
		":90:WPA2", // hidden
		`Colon\:Net:40:WPA1 WPA2`,
	}, "\n")
	got := parseScan(out)
	want := []Network{
		{SSID: "Home", Signal: 81, Secure: true},
		{SSID: "Cafe", Signal: 60, Secure: false},
		{SSID: "Colon:Net", Signal: 40, Secure: true},
		{SSID: "Old", Signal: 30, Secure: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseScan =\n%+v\nwant\n%+v", got, want)
	}
}

func TestStartAP(t *testing.T) {
	s := &script{}
	if err := newScripted(s).StartAP("wlan0", "Syncphony-Box-ABCD"); err != nil {
		t.Fatal(err)
	}
	var add string
	for _, c := range s.calls {
		if strings.HasPrefix(c, "connection add") {
			add = c
		}
	}
	for _, want := range []string{
		"save no", "ifname wlan0", "con-name " + APConnection, "ssid Syncphony-Box-ABCD",
		"802-11-wireless.mode ap", "ipv4.method shared", "ipv4.addresses 10.42.0.1/24",
		"autoconnect no",
	} {
		if !strings.Contains(add, want) {
			t.Errorf("connection add missing %q: %s", want, add)
		}
	}
	if strings.Contains(add, "wifi-sec") {
		t.Error("the setup AP must be open")
	}
	if last := s.calls[len(s.calls)-1]; last != "connection up id "+APConnection {
		t.Errorf("last call = %q, want the AP brought up", last)
	}
	if s.calls[0] != "radio wifi on" {
		t.Errorf("first call = %q, want the radio unblocked first", s.calls[0])
	}
}

func TestStartAPFailureCleansUp(t *testing.T) {
	s := &script{fail: map[string]error{"connection up": errors.New("no AP support")}}
	if err := newScripted(s).StartAP("wlan0", "X"); err == nil {
		t.Fatal("want an error")
	}
	if last := s.calls[len(s.calls)-1]; last != "connection delete id "+APConnection {
		t.Errorf("last call = %q, want the half-made AP deleted", last)
	}
}

func TestConnect(t *testing.T) {
	s := &script{}
	if err := newScripted(s).Connect("wlan0", "Home", "hunter22", false); err != nil {
		t.Fatal(err)
	}
	want := "--wait 45 device wifi connect Home password hunter22 ifname wlan0 name Home"
	found := false
	for _, c := range s.calls {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Errorf("no %q in %q", want, s.calls)
	}

	s = &script{}
	newScripted(s).Connect("wlan0", "Open", "", true)
	if c := s.calls[len(s.calls)-1]; c != "--wait 45 device wifi connect Open ifname wlan0 name Open hidden yes" {
		t.Errorf("open hidden connect = %q", c)
	}
}

func TestConnectWaitsForTheNetworkToShow(t *testing.T) {
	slept := 0
	prev := sleep
	sleep = func(time.Duration) { slept++ }
	defer func() { sleep = prev }()

	// In the list (a comma in the name is just a character to nmcli).
	ssid := "my cool, big network"
	s := &script{replies: map[string]string{"-t -f SSID device wifi list": "Other\n" + ssid + "\n"}}
	if err := newScripted(s).Connect("wlan0", ssid, "hunter22", false); err != nil {
		t.Fatal(err)
	}
	scans := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, "-t -f SSID device wifi list ifname wlan0 --rescan yes") {
			scans++
		}
	}
	if scans != 1 || slept != 0 {
		t.Errorf("scans = %d, slept = %d; want one scan, no wait", scans, slept)
	}
	if last := s.calls[len(s.calls)-1]; last != "--wait 45 device wifi connect "+ssid+" password hunter22 ifname wlan0 name "+ssid {
		t.Errorf("connect = %q", last)
	}

	// Never shows: a few scans, then the connect anyway (its error says why).
	s = &script{replies: map[string]string{"-t -f SSID device wifi list": "Other\n"}}
	slept = 0
	newScripted(s).Connect("wlan0", "Home", "hunter22", false)
	scans = 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, "-t -f SSID device wifi list") {
			scans++
		}
	}
	if scans != visibleTries || slept != visibleTries-1 || !strings.HasPrefix(s.calls[len(s.calls)-1], "--wait 45 device wifi connect Home") {
		t.Errorf("scans = %d, slept = %d, calls %q", scans, slept, s.calls)
	}
}

func TestConnectFailure(t *testing.T) {
	s := &script{fail: map[string]error{
		"--wait": errors.New("Error: Connection activation failed: Secrets were required, but not provided."),
	}}
	err := newScripted(s).Connect("wlan0", "Home", "wrong", false)
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("err = %v, want a password hint", err)
	}
	if last := s.calls[len(s.calls)-1]; last != "connection delete id Home" {
		t.Errorf("last call = %q, want the failed connection removed", last)
	}
}

func TestJoinString(t *testing.T) {
	if got := JoinString("Syncphony-Box-ABCD"); got != "WIFI:S:Syncphony-Box-ABCD;T:nopass;;" {
		t.Errorf("JoinString = %q", got)
	}
	if got := JoinString(`a;b:c`); got != `WIFI:S:a\;b\:c;T:nopass;;` {
		t.Errorf("JoinString escaping = %q", got)
	}
}
