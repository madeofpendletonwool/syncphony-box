// Package config is the one place that knows the syncphony.txt format
// (MAD-800, ADR 0003). boxd took the parsing over from the stage-1 shell
// script and must keep its exact behavior; tests/syncphony-box-config.bats
// pins it from the outside, and these tests pin it from the inside.
package config

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Default file paths; overridable through the SB_* environment hooks so the
// apply path can be tested on a dev machine instead of a box.
const (
	DefaultTxtPath     = "/boot/firmware/syncphony.txt"
	DefaultEnvPath     = "/etc/syncphony-box/config.env"
	DefaultCmdlinePath = "/boot/firmware/cmdline.txt"
)

// Config is the validated result of parsing syncphony.txt. Invalid values
// fall back to defaults with a warning; parsing never fails.
type Config struct {
	ServerURL  string
	Name       string
	Hostname   string // slug of Name; empty when Name is unset or unusable
	Audio      string
	Resolution string
	Rotate     string
	CEC        string
	Warnings   []string
}

// Defaults returns the config when nothing is set in the file.
func Defaults() Config {
	return Config{Audio: "hdmi", Rotate: "0", CEC: "on"}
}

func (c *Config) warn(format string, args ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
}

// ParseFile parses a syncphony.txt. A missing or unreadable file yields the
// defaults (plus a warning when the file exists but can't be read; a plain
// missing file is normal). It never fails.
func ParseFile(path string) Config {
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			c := Defaults()
			c.warn("cannot read %s: %v", path, err)
			return c
		}
		return Defaults()
	}
	defer f.Close()
	return Parse(f)
}

// Parse parses syncphony.txt content from a reader: UTF-8 BOM stripped, CRLF
// line endings accepted (people edit this file in Notepad), comments and
// blank lines skipped, whitespace around keys and values trimmed, keys
// case-insensitive, later duplicates win, unknown keys and unrecognized
// lines warn and are ignored.
func Parse(r io.Reader) Config {
	c := Defaults()
	data, err := io.ReadAll(r)
	if err != nil {
		c.warn("read error: %v", err)
		return c
	}
	data = stripBOM(data)
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			c.warn("ignoring unrecognized line: %s", line)
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		value := strings.TrimSpace(line[eq+1:])
		c.apply(key, value)
	}
	return c
}

func (c *Config) apply(key, value string) {
	switch key {
	case "server_url":
		switch {
		case value == "":
		case strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://"):
			c.ServerURL = value
		default:
			c.warn("invalid server_url '%s' (want https://syncphony.example.com); ignoring it", value)
		}
	case "name":
		c.Name = value
		c.Hostname = Slugify(value)
		if c.Name != "" && c.Hostname == "" {
			c.warn("name '%s' has no letters or digits; leaving the hostname alone", value)
		}
	case "audio":
		switch value {
		case "":
		case "hdmi", "hdmi2", "analog", "usb":
			c.Audio = value
		default:
			c.warn("invalid audio '%s' (want hdmi, hdmi2, analog or usb); using %s", value, c.Audio)
		}
	case "resolution":
		if value != "" && !resolutionRE.MatchString(value) {
			c.warn("invalid resolution '%s' (want e.g. 1920x1080 or 1920x1080@60); leaving it on auto", value)
		} else {
			c.Resolution = value
		}
	case "rotate":
		value = strings.ToLower(value)
		switch value {
		case "":
		case "0", "90", "180", "270":
			c.Rotate = value
		default:
			c.warn("invalid rotate '%s' (want 0, 90, 180 or 270); using %s", value, c.Rotate)
		}
	case "cec":
		value = strings.ToLower(value)
		switch value {
		case "":
		case "on", "off":
			c.CEC = value
		default:
			c.warn("invalid cec '%s' (want on or off); using %s", value, c.CEC)
		}
	default:
		c.warn("ignoring unknown key '%s'", key)
	}
}

var resolutionRE = regexp.MustCompile(`^[0-9]{3,5}x[0-9]{3,5}(@[0-9]{1,3})?$`)

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		return b[3:]
	}
	return b
}

// Slugify turns "Living room TV" into living-room-tv: case folded, runs of
// anything but [a-z0-9] collapsed to a single hyphen, trimmed of hyphens,
// capped at a hostname label's 63 characters. Empty result means the name
// had nothing usable.
func Slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen && b.Len() > 0 {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	slug := b.String()
	if len(slug) > 63 {
		slug = slug[:63]
	}
	return strings.Trim(slug, "-")
}

// EnvContent renders the config as a shell-sourceable config.env (the
// kiosk wrapper reads it): single-quoted values, so names like O'Neil's TV
// survive being sourced.
func (c Config) EnvContent() string {
	var b strings.Builder
	b.WriteString("# Generated by boxd from syncphony.txt. Edit syncphony.txt on the\n# boot partition instead.\n")
	fmt.Fprintf(&b, "SERVER_URL=%s\n", shellQuote(c.ServerURL))
	fmt.Fprintf(&b, "NAME=%s\n", shellQuote(c.Name))
	fmt.Fprintf(&b, "HOSTNAME=%s\n", shellQuote(c.Hostname))
	fmt.Fprintf(&b, "AUDIO=%s\n", shellQuote(c.Audio))
	fmt.Fprintf(&b, "RESOLUTION=%s\n", shellQuote(c.Resolution))
	fmt.Fprintf(&b, "ROTATE=%s\n", shellQuote(c.Rotate))
	fmt.Fprintf(&b, "CEC=%s\n", shellQuote(c.CEC))
	return b.String()
}

// shellQuote wraps a value in single quotes for POSIX sh, with embedded
// quotes escaped as '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// VideoToken returns the desired kernel cmdline token for resolution and
// rotate: video=HDMI-A-1:<mode>D, plus ,rotate=N when rotated. The trailing
// D forces the output on, so a TV that's off at boot still gets a picture
// later. Empty means auto: no token at all.
func VideoToken(resolution, rotate string) string {
	token := ""
	if resolution != "" {
		token = "video=HDMI-A-1:" + resolution + "D"
	}
	switch rotate {
	case "90", "180", "270":
		if token != "" {
			token += ",rotate=" + rotate
		} else {
			token = "video=HDMI-A-1:D,rotate=" + rotate
		}
	}
	return token
}
