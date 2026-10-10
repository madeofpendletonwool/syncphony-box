package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parse(t *testing.T, content string) Config {
	t.Helper()
	return Parse(strings.NewReader(content))
}

func wantWarning(t *testing.T, c Config, substr string) {
	t.Helper()
	for _, w := range c.Warnings {
		if strings.Contains(w, substr) {
			return
		}
	}
	t.Errorf("no warning containing %q; got %v", substr, c.Warnings)
}

func TestMissingFileYieldsDefaults(t *testing.T) {
	c := ParseFile(filepath.Join(t.TempDir(), "nope.txt"))
	if c.ServerURL != "" || c.Name != "" || c.Hostname != "" ||
		c.Audio != "hdmi" || c.Resolution != "" || c.Rotate != "0" || c.CEC != "on" {
		t.Fatalf("got %+v", c)
	}
}

func TestBOMAndCRLF(t *testing.T) {
	c := parse(t, "\xef\xbb\xbfserver_url=https://s.example\r\nname=My TV\r\naudio=usb\r\n")
	if c.ServerURL != "https://s.example" || c.Name != "My TV" || c.Hostname != "my-tv" || c.Audio != "usb" {
		t.Fatalf("got %+v", c)
	}
}

func TestAllKeysTogether(t *testing.T) {
	c := parse(t, "server_url=https://s.example\nname=Den TV\naudio=analog\nresolution=1920x1080@60\nrotate=180\ncec=off\n")
	if c.ServerURL != "https://s.example" || c.Name != "Den TV" || c.Hostname != "den-tv" ||
		c.Audio != "analog" || c.Resolution != "1920x1080@60" || c.Rotate != "180" || c.CEC != "off" {
		t.Fatalf("got %+v", c)
	}
}

func TestUnknownKeysAndLinesWarn(t *testing.T) {
	c := parse(t, "server_url=https://s.example\nwhatever=1\nno_equals_sign\n")
	wantWarning(t, c, "ignoring unknown key 'whatever'")
	wantWarning(t, c, "ignoring unrecognized line: no_equals_sign")
}

func TestInvalidValuesFallBack(t *testing.T) {
	c := parse(t, "server_url=ftp://nope.example\naudio=spdif\nrotate=45\ncec=yes\nresolution=1080p\n")
	if c.ServerURL != "" || c.Audio != "hdmi" || c.Rotate != "0" || c.CEC != "on" || c.Resolution != "" {
		t.Fatalf("got %+v", c)
	}
	wantWarning(t, c, "invalid server_url 'ftp://nope.example'")
	wantWarning(t, c, "invalid audio 'spdif'")
	wantWarning(t, c, "invalid rotate '45'")
	wantWarning(t, c, "invalid cec 'yes'")
	wantWarning(t, c, "invalid resolution '1080p'")
}

func TestDuplicateLastWinsAndCaseInsensitive(t *testing.T) {
	c := parse(t, "server_url=https://first.example\nserver_url=https://second.example\n  SERVER_URL = https://third.example  \n")
	if c.ServerURL != "https://third.example" {
		t.Fatalf("got %q", c.ServerURL)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Living room TV": "living-room-tv",
		"Café & Bar!":    "caf-bar",
		"  --Den--  ":    "den",
		"???":            "",
		"O'Neil's TV":    "o-neil-s-tv",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvContentIsSourceable(t *testing.T) {
	c := parse(t, "name=O'Neil's TV\nserver_url=https://s.example\n")
	env := c.EnvContent()
	for want := range map[string]bool{
		"SERVER_URL='https://s.example'": true,
		"NAME='O'\\''Neil'\\''s TV'":     true,
		"HOSTNAME='o-neil-s-tv'":         true,
		"AUDIO='hdmi'":                   true,
		"ROTATE='0'":                     true,
		"CEC='on'":                       true,
	} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("env missing %q:\n%s", want, env)
		}
	}
}

func TestVideoToken(t *testing.T) {
	for _, tc := range []struct{ res, rot, want string }{
		{"1920x1080@60", "0", "video=HDMI-A-1:1920x1080@60D"},
		{"1920x1080@60", "270", "video=HDMI-A-1:1920x1080@60D,rotate=270"},
		{"", "90", "video=HDMI-A-1:D,rotate=90"},
		{"1280x720", "0", "video=HDMI-A-1:1280x720D"},
		{"", "0", ""},
	} {
		if got := VideoToken(tc.res, tc.rot); got != tc.want {
			t.Errorf("VideoToken(%q,%q) = %q, want %q", tc.res, tc.rot, got, tc.want)
		}
	}
}

func TestTargetURL(t *testing.T) {
	got := TargetURL("https://s.example/", "1.2.3", "Living room TV")
	want := "https://s.example/tv?box=1.2.3&name=Living%20room%20TV"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := TargetURL("https://s.example/tv", "dev", ""); got != "https://s.example/tv?box=dev" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyWritesEnvAndSyncsCmdline(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "syncphony.txt")
	env := filepath.Join(dir, "config.env")
	cmdline := filepath.Join(dir, "cmdline.txt")
	hostname := filepath.Join(dir, "hostname")
	rebooted := filepath.Join(dir, "rebooted")

	os.WriteFile(cmdline, []byte("console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4\n"), 0o644)
	os.WriteFile(txt, []byte("server_url=https://s.example\nresolution=1920x1080@60\n"), 0o644)

	p := Paths{Txt: txt, Env: env, Cmdline: cmdline,
		HostnameCmd: "printf %s \"$1\" > " + hostname,
		RebootCmd:   "touch " + rebooted}
	var ran []string
	Apply(p, func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}, testLogger())

	// config.env written and sourceable.
	data, _ := os.ReadFile(env)
	if !strings.Contains(string(data), "SERVER_URL='https://s.example'") {
		t.Fatalf("config.env: %s", data)
	}
	// hostname set from name= only when present — no name here.
	if _, err := os.Stat(hostname); !os.IsNotExist(err) {
		t.Fatal("hostname was set without name=")
	}
	// cmdline gained the video= token and rebooted exactly once.
	got, _ := os.ReadFile(cmdline)
	want := "console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4 video=HDMI-A-1:1920x1080@60D\n"
	if string(got) != want {
		t.Fatalf("cmdline = %q, want %q", got, want)
	}
	if _, err := os.Stat(rebooted); err != nil {
		t.Fatal("did not reboot on a cmdline change")
	}

	// Second apply with the same config: no-op, no reboot loop.
	os.Remove(rebooted)
	Apply(p, func(name string, args ...string) error { return nil }, testLogger())
	if _, err := os.Stat(rebooted); !os.IsNotExist(err) {
		t.Fatal("rebooted again for an unchanged cmdline")
	}
}

func TestApplySetsHostnameFromName(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "syncphony.txt")
	env := filepath.Join(dir, "config.env")
	hostname := filepath.Join(dir, "hostname")
	os.WriteFile(txt, []byte("name=Living room TV\n"), 0o644)

	p := Paths{Txt: txt, Env: env, Cmdline: filepath.Join(dir, "cmdline.txt"),
		HostnameCmd: "printf %s \"$1\" > " + hostname}
	Apply(p, func(string, ...string) error { return nil }, testLogger())
	got, _ := os.ReadFile(hostname)
	if string(got) != "living-room-tv" {
		t.Fatalf("hostname = %q", got)
	}
}

func TestSetKeysPreservesCommentsAndEndings(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "syncphony.txt")
	os.WriteFile(txt, []byte("\xef\xbb\xbf# Syncphony Box settings.\r\n#\r\n# The Syncphony server this box shows. Required.\r\n#server_url=https://syncphony.example.com\r\n# The box's name in the room's Screens list (also the hostname, slugified).\r\n#name=Living room TV\r\naudio=hdmi\r\n"), 0o644)

	if err := SetKeys(txt,
		KV{Key: "server_url", Value: "https://music.example.com"},
		KV{Key: "name", Value: "Den TV"},
	); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(txt)
	s := string(data)
	if !strings.HasPrefix(s, "\xef\xbb\xbf") {
		t.Error("BOM lost")
	}
	if !strings.Contains(s, "\r\n") {
		t.Error("CRLF lost")
	}
	if !strings.Contains(s, "server_url=https://music.example.com\r\n") {
		t.Errorf("server_url not activated:\n%s", s)
	}
	if !strings.Contains(s, "name=Den TV\r\n") {
		t.Errorf("name not activated:\n%s", s)
	}
	if !strings.Contains(s, "# Syncphony Box settings.") {
		t.Error("comments lost")
	}
	if !strings.Contains(s, "audio=hdmi") {
		t.Error("other keys lost")
	}
	// And the result parses back to what was saved.
	c := ParseFile(txt)
	if c.ServerURL != "https://music.example.com" || c.Name != "Den TV" || c.Hostname != "den-tv" || c.Audio != "hdmi" {
		t.Fatalf("reparse got %+v", c)
	}
}

func TestSetKeysReplacesActiveValue(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "syncphony.txt")
	os.WriteFile(txt, []byte("server_url=https://old.example\n"), 0o644)
	if err := SetKeys(txt, KV{Key: "server_url", Value: "https://new.example"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(txt)
	if strings.Contains(string(data), "old.example") {
		t.Fatalf("old value survived: %s", data)
	}
	if !strings.Contains(string(data), "server_url=https://new.example") {
		t.Fatalf("new value missing: %s", data)
	}
}
