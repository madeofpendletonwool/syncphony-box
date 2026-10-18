// Package wifi is boxd's NetworkManager half (MAD-808, ADR 0010): just
// enough nmcli to set up a box that has no network at all. It reports
// whether the box is online (an activated Ethernet or Wi-Fi connection),
// scans for networks, brings up the open setup access point
// Syncphony-Box-XXXX in NetworkManager's shared mode (DHCP and DNS from
// NetworkManager's own dnsmasq; the image's dnsmasq-shared.d drop-in
// turns that DNS into a catch-all pointing at boxd), and joins the
// network a phone chose.
//
// Everything goes through one nmcli func field, stubbed in tests, in the
// spirit of the cec package. A machine without NetworkManager (a dev
// box) gets errors back and boxd runs with all of this inert.
package wifi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// APConnection is the NetworkManager connection the setup access
	// point runs as. It is created in memory only (save no), so it never
	// reaches /etc/NetworkManager/system-connections — nor /data, which
	// data-persist snapshots from there.
	APConnection = "syncphony-setup"

	// APAddress is the box's address on its own access point: the
	// gateway, the DHCP server, the DNS catch-all's answer and the
	// captive portal. Pinned rather than left to NetworkManager's
	// shared-mode default, so the dnsmasq drop-in can name it.
	APAddress = "10.42.0.1"

	// APChannel is a 2.4 GHz channel legal in every regulatory domain,
	// including the "world" domain a box without a Wi-Fi country runs in.
	APChannel = "6"

	// connectWait bounds a join attempt: association, the 4-way
	// handshake and DHCP on a slow router fit comfortably.
	connectWait = 45

	// visibleTries rescans, visiblePause apart, wait for the network
	// before a join.
	visibleTries = 4
)

var (
	visiblePause = 3 * time.Second
	sleep        = time.Sleep // stubbed in tests
)

// Network is one scanned Wi-Fi network, deduplicated by SSID.
type Network struct {
	SSID   string
	Signal int  // 0-100
	Secure bool // needs a password (WPA/WEP); false for an open network
}

// NM drives NetworkManager through nmcli.
type NM struct {
	// nmcli runs nmcli with args and returns its stdout; on failure the
	// error carries nmcli's message. A func field so tests can script it.
	nmcli func(args ...string) (string, error)
	// readFile reads /sys (the Wi-Fi MAC); stubbed in tests.
	readFile func(string) ([]byte, error)
}

// New returns an NM that runs the real nmcli.
func New() *NM {
	return &NM{nmcli: runNmcli, readFile: os.ReadFile}
}

// NewWith returns an NM over a scripted nmcli (tests, and the daemon's
// own tests through it).
func NewWith(nmcli func(args ...string) (string, error), readFile func(string) ([]byte, error)) *NM {
	return &NM{nmcli: nmcli, readFile: readFile}
}

func runNmcli(args ...string) (string, error) {
	// The join waits up to connectWait itself; everything else is quick.
	ctx, cancel := context.WithTimeout(context.Background(), (connectWait+15)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nmcli", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), errors.New(msg)
		}
		return string(out), err
	}
	return string(out), nil
}

// Device returns the first Wi-Fi interface NetworkManager manages
// (wlan0 on a Pi), or an error when there is none or no NetworkManager.
func (n *NM) Device() (string, error) {
	out, err := n.nmcli("-t", "-f", "DEVICE,TYPE", "device", "status")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		f := splitTerse(line)
		if len(f) >= 2 && f[1] == "wifi" {
			return f[0], nil
		}
	}
	return "", errors.New("no Wi-Fi device")
}

// Online reports whether an Ethernet or Wi-Fi connection — other than
// the setup access point — is fully activated (link up and addressed).
func (n *NM) Online() (bool, error) {
	out, err := n.nmcli("-t", "-f", "NAME,TYPE,STATE", "connection", "show", "--active")
	if err != nil {
		return false, err
	}
	return parseOnline(out), nil
}

func parseOnline(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := splitTerse(line)
		if len(f) < 3 || f[0] == APConnection || f[2] != "activated" {
			continue
		}
		if f[1] == "802-3-ethernet" || f[1] == "802-11-wireless" {
			return true
		}
	}
	return false
}

// SavedWiFi reports whether NetworkManager knows any Wi-Fi network to
// join on its own (the Imager settings, an earlier setup) — the reason
// to give the access point up now and then and look for it again.
func (n *NM) SavedWiFi() bool {
	out, err := n.nmcli("-t", "-f", "NAME,TYPE", "connection", "show")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		f := splitTerse(line)
		if len(f) >= 2 && f[0] != APConnection && f[1] == "802-11-wireless" {
			return true
		}
	}
	return false
}

// APSSID is the setup network's name: Syncphony-Box- and the last four
// hex digits of the Wi-Fi MAC, so two boxes in one house stay apart.
func (n *NM) APSSID(dev string) string {
	data, err := n.readFile("/sys/class/net/" + dev + "/address")
	hex := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(string(data)), ":", ""))
	if err != nil || len(hex) < 4 {
		return "Syncphony-Box"
	}
	return "Syncphony-Box-" + hex[len(hex)-4:]
}

// Scan rescans and lists the networks in range, strongest first, one
// entry per SSID. It needs the radio free: call it before the access
// point comes up.
func (n *NM) Scan(dev string) ([]Network, error) {
	n.nmcli("radio", "wifi", "on")
	out, err := n.nmcli("-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list",
		"ifname", dev, "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	return parseScan(out), nil
}

func parseScan(out string) []Network {
	best := map[string]Network{}
	for _, line := range strings.Split(out, "\n") {
		f := splitTerse(line)
		if len(f) < 3 || f[0] == "" {
			continue // hidden networks show no SSID; the form has a field for those
		}
		sig, _ := strconv.Atoi(f[1])
		sec := f[2] != "" && f[2] != "--"
		if cur, ok := best[f[0]]; !ok || sig > cur.Signal {
			best[f[0]] = Network{SSID: f[0], Signal: sig, Secure: sec}
		}
	}
	nets := make([]Network, 0, len(best))
	for _, nw := range best {
		nets = append(nets, nw)
	}
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Signal != nets[j].Signal {
			return nets[i].Signal > nets[j].Signal
		}
		return nets[i].SSID < nets[j].SSID
	})
	return nets
}

// StartAP brings up the open setup network on dev: in memory only, 2.4
// GHz, shared mode at APAddress.
func (n *NM) StartAP(dev, ssid string) error {
	n.nmcli("radio", "wifi", "on") // a box flashed without a Wi-Fi country starts soft-blocked
	n.nmcli("connection", "delete", "id", APConnection)
	if _, err := n.nmcli("connection", "add", "save", "no",
		"type", "wifi", "ifname", dev, "con-name", APConnection,
		"autoconnect", "no", "ssid", ssid,
		"802-11-wireless.mode", "ap",
		"802-11-wireless.band", "bg",
		"802-11-wireless.channel", APChannel,
		"ipv4.method", "shared",
		"ipv4.addresses", APAddress+"/24",
		"ipv6.method", "disabled"); err != nil {
		return fmt.Errorf("add access point: %w", err)
	}
	if _, err := n.nmcli("connection", "up", "id", APConnection); err != nil {
		n.nmcli("connection", "delete", "id", APConnection)
		return fmt.Errorf("start access point: %w", err)
	}
	return nil
}

// StopAP takes the setup network down and forgets it.
func (n *NM) StopAP() error {
	_, err := n.nmcli("connection", "delete", "id", APConnection)
	return err
}

// Connect joins ssid and saves it as a NetworkManager connection named
// after the network, replacing any earlier one of that name, so it
// autoconnects from now on. hidden is for a network that doesn't
// broadcast its name. On failure the half-made connection is removed
// and the error says what went wrong in words for the TV.
func (n *NM) Connect(dev, ssid, password string, hidden bool) error {
	n.nmcli("radio", "wifi", "on")
	n.nmcli("connection", "delete", "id", ssid)
	if !hidden {
		n.waitVisible(dev, ssid)
	}
	args := []string{"--wait", strconv.Itoa(connectWait), "device", "wifi", "connect", ssid}
	if password != "" {
		args = append(args, "password", password)
	}
	args = append(args, "ifname", dev, "name", ssid)
	if hidden {
		args = append(args, "hidden", "yes")
	}
	if _, err := n.nmcli(args...); err != nil {
		n.nmcli("connection", "delete", "id", ssid)
		return friendlyError(ssid, err)
	}
	return nil
}

// waitVisible rescans until ssid is in the scan list, a few times at
// most. Right after the access point goes down the list is empty, and a
// plain "wifi rescan" returns before the scan ends, so the connect that
// followed failed with "No network with SSID" for a network in range
// ("--rescan yes" waits for the scan). A network that never shows still
// gets the connect, whose error says why.
func (n *NM) waitVisible(dev, ssid string) {
	for i := 0; i < visibleTries; i++ {
		if i > 0 {
			sleep(visiblePause)
		}
		out, err := n.nmcli("-t", "-f", "SSID", "device", "wifi", "list", "ifname", dev, "--rescan", "yes")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			if f := splitTerse(line); len(f) > 0 && f[0] == ssid {
				return
			}
		}
	}
}

// friendlyError turns nmcli's activation errors into something a person
// on a couch can act on.
func friendlyError(ssid string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Secrets were required"),
		strings.Contains(msg, "802.1X supplicant"),
		strings.Contains(msg, "802-11-wireless-security.psk"):
		return fmt.Errorf("couldn't join %s — is the password right?", ssid)
	case strings.Contains(msg, "No network with SSID"):
		return fmt.Errorf("couldn't find %s — is it in range, and is the name exact?", ssid)
	case strings.Contains(msg, "IP configuration could not be reserved"),
		strings.Contains(msg, "ip-config-unavailable"):
		return fmt.Errorf("joined %s but got no address from the router", ssid)
	case strings.Contains(msg, "Timeout"), strings.Contains(msg, "timed out"):
		return fmt.Errorf("couldn't join %s in time — is it in range?", ssid)
	}
	return fmt.Errorf("couldn't join %s (%s)", ssid, msg)
}

// JoinString is the Wi-Fi QR payload for an open network: a phone camera
// offers to join it straight from the TV.
func JoinString(ssid string) string {
	return "WIFI:S:" + escapeQR(ssid) + ";T:nopass;;"
}

func escapeQR(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\;,:"`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// splitTerse splits one line of nmcli's terse (-t) output: fields are
// separated by ':', and ':' and '\' inside a field are escaped with '\'.
func splitTerse(line string) []string {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return nil
	}
	var fields []string
	var cur strings.Builder
	esc := false
	for _, r := range line {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(fields, cur.String())
}
