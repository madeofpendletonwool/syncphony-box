package config

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Paths resolves the SB_* test hooks: SB_TXT, SB_ENV, SB_CMDLINE, SB_HOSTS
// override the real file locations, SB_PROFILE the Chromium profile dir, and
// SB_HOSTNAME_CMD / SB_REBOOT_CMD replace the privileged actions, so
// `boxd config apply` can run on a dev machine.
type Paths struct {
	Txt         string
	Env         string
	Cmdline     string
	Hosts       string // empty = leave /etc/hosts alone (tests)
	ProfileDir  string // empty = DefaultProfileDir
	HostnameCmd string // run as: sh -c "$HostnameCmd" boxd <hostname>
	RebootCmd   string // run as: sh -c "$RebootCmd"
}

func PathsFromEnv() Paths {
	return Paths{
		Txt:         envOr("SB_TXT", DefaultTxtPath),
		Env:         envOr("SB_ENV", DefaultEnvPath),
		Cmdline:     envOr("SB_CMDLINE", defaultCmdlinePath()),
		Hosts:       envOr("SB_HOSTS", DefaultHostsPath),
		ProfileDir:  os.Getenv("SB_PROFILE"),
		HostnameCmd: os.Getenv("SB_HOSTNAME_CMD"),
		RebootCmd:   os.Getenv("SB_REBOOT_CMD"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// defaultCmdlinePath picks the box's kernel command line file: GRUB's
// cmdline.cfg on an installed x86 box, the Pi's cmdline.txt otherwise.
func defaultCmdlinePath() string {
	if _, err := os.Stat(DefaultGrubCmdlinePath); err == nil {
		return DefaultGrubCmdlinePath
	}
	return DefaultCmdlinePath
}

// grubCmdlineVar is the variable cmdline.cfg sets; the x86 grub.cfg
// passes ${syncphony_cmdline} to the kernel.
const grubCmdlineVar = "syncphony_cmdline"

// isGrubCmdline reports whether a command line file is the x86 GRUB
// script (cmdline.cfg) rather than the Pi's one-line cmdline.txt.
func isGrubCmdline(path string) bool {
	return strings.HasSuffix(path, ".cfg")
}

// cmdlineTokens extracts the kernel command line from a file's content:
// the whole file for cmdline.txt, the quoted value of the
// `set syncphony_cmdline="..."` line for cmdline.cfg.
func cmdlineTokens(path, content string) string {
	if !isGrubCmdline(path) {
		return strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, content)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "set "+grubCmdlineVar+"="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// cmdlineContent is cmdlineTokens' inverse: the new file content for a
// command line. cmdline.cfg keeps its other lines (the comments) and
// gets its set line replaced, or appended when it had none.
func cmdlineContent(path, old, line string) string {
	if !isGrubCmdline(path) {
		return line + "\n"
	}
	set := "set " + grubCmdlineVar + `="` + line + `"`
	lines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "set "+grubCmdlineVar+"=") {
			lines[i] = set
			return strings.Join(lines, "\n") + "\n"
		}
	}
	if old == "" {
		return set + "\n"
	}
	return strings.Join(append(lines, set), "\n") + "\n"
}

// RunCommand runs an external command (systemctl and friends). A stub in
// tests records what would have run on the box.
type RunCommand func(name string, args ...string) error

// RealRunCommand runs commands for real.
func RealRunCommand(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// Apply is the boot path (the old syncphony-box-config apply, behavior
// identical): parse syncphony.txt, write config.env, set the hostname from
// name=, sync the video= token into the kernel command line and reboot once
// if it changed. Every bad-config path logs and continues; it never fails.
func Apply(p Paths, run RunCommand, log *log.Logger) {
	cfg := ParseFile(p.Txt)
	if p.ProfileDir != "" {
		cfg.ProfileDir = p.ProfileDir // SB_PROFILE dev/test override
	}
	if err := WriteEnvFile(p.Env, cfg); err != nil {
		log.Printf("boxd config: cannot write %s; the kiosk falls back to defaults: %v", p.Env, err)
	}
	// Like the shell script, the values below come from the env file just
	// written — if the write failed, from whatever config.env was already
	// there, so a read-only /etc keeps the last good settings.
	envCfg := readEnvFile(p.Env)

	if envCfg.Hostname != "" {
		setHostname(envCfg.Hostname, p, run, log)
	}

	syncCmdline(envCfg.Resolution, envCfg.Rotate, p, run, log)
}

// WriteEnvFile atomically writes config.env (tmp file + rename).
func WriteEnvFile(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileSync(tmp, cfg.EnvContent(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeFileSync(path string, content string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readEnvFile reads back a config.env this package (or the stage-1 script)
// wrote, for HOSTNAME, RESOLUTION and ROTATE.
func readEnvFile(path string) Config {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "HOSTNAME=") &&
			!strings.HasPrefix(line, "RESOLUTION=") &&
			!strings.HasPrefix(line, "ROTATE=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		value = strings.TrimPrefix(value, "'")
		value = strings.TrimSuffix(value, "'")
		value = strings.ReplaceAll(value, `'\''`, `'`)
		switch key {
		case "HOSTNAME":
			cfg.Hostname = value
		case "RESOLUTION":
			cfg.Resolution = value
		case "ROTATE":
			cfg.Rotate = value
		}
	}
	return cfg
}

// SetHostname applies a new hostname: the running name, /etc/hostname
// and the 127.0.1.1 line in /etc/hosts. Without that line the box can't
// resolve its own name (sudo complains on every call). Both the boot path
// and boxd's setup save use it.
func SetHostname(hostname string, p Paths, run RunCommand, log *log.Logger) {
	setHostname(hostname, p, run, log)
}

func setHostname(hostname string, p Paths, run RunCommand, log *log.Logger) {
	if p.HostnameCmd != "" {
		if err := runSh(p.HostnameCmd, "boxd", hostname, run); err != nil {
			log.Printf("boxd config: SB_HOSTNAME_CMD failed: %v", err)
		}
	} else if _, err := exec.LookPath("hostnamectl"); err == nil &&
		run("hostnamectl", "set-hostname", hostname) == nil {
		// hostnamectl worked.
	} else if err := os.WriteFile("/etc/hostname", []byte(hostname+"\n"), 0o644); err != nil {
		log.Printf("boxd config: cannot write /etc/hostname: %v", err)
	} else if err := run("hostname", hostname); err != nil {
		log.Printf("boxd config: cannot set the running hostname: %v", err)
	}
	if p.Hosts != "" {
		if err := SyncHosts(p.Hosts, hostname); err != nil {
			log.Printf("boxd config: cannot update %s: %v", p.Hosts, err)
		}
	}
	// If avahi managed to start first, re-announce under the new name.
	run("systemctl", "try-restart", "avahi-daemon.service")
}

func runSh(command, arg0, arg1 string, run RunCommand) error {
	return exec.Command("sh", "-c", command, arg0, arg1).Run()
}

// syncCmdline syncs the video=HDMI-A-1:... token into the kernel command
// line: drop any previous one, append the desired one. Changing it takes a
// reboot, which happens exactly once — after the write the next boot
// computes the same token and finds nothing to do.
//
// No command line file at all means no bootloader reads one: the x86 live
// session boots from the read-only ISO (MAD-846). Writing one there would
// only cost a pointless reboot, so the display settings are skipped.
func syncCmdline(resolution, rotate string, p Paths, run RunCommand, log *log.Logger) {
	desired := VideoToken(resolution, rotate)
	current := ""
	old := ""
	if data, err := os.ReadFile(p.Cmdline); err == nil {
		old = string(data)
		current = cmdlineTokens(p.Cmdline, old)
	} else if os.IsNotExist(err) {
		if desired != "" {
			log.Printf("boxd config: no kernel command line here (%s); resolution and rotate are not applied", p.Cmdline)
		}
		return
	}
	// Otherwise unreadable (a directory, say): behave as empty and let the
	// write below fail loudly instead.

	var kept []string
	for _, tok := range strings.Fields(current) {
		if !strings.HasPrefix(tok, "video=HDMI-A-1:") {
			kept = append(kept, tok)
		}
	}
	desiredDesc := desired
	if desiredDesc == "" {
		desiredDesc = "video= removed"
	}
	next := strings.Join(kept, " ")
	if desired != "" {
		if next != "" {
			next += " "
		}
		next += desired
	}
	if next == current {
		return
	}
	// /boot/firmware stays read-only (ADR 0007): the write goes through the
	// remount dance, so a power pull can never catch the FAT half-written.
	err := WithMountWritable(p.Cmdline, run, log, func() error {
		return os.WriteFile(p.Cmdline, []byte(cmdlineContent(p.Cmdline, old, next)), 0o644)
	})
	if err != nil {
		log.Printf("boxd config: cannot write %s; display settings stay as they are: %v", p.Cmdline, err)
		return
	}
	log.Printf("boxd config: kernel cmdline changed (%s); rebooting once so it takes effect", desiredDesc)
	if p.RebootCmd != "" {
		exec.Command("sh", "-c", p.RebootCmd).Run()
		return
	}
	run("systemctl", "reboot")
}

// KV is one key=value pair for SetKeys, in a fixed order.
type KV struct {
	Key   string
	Value string
}

// SetKeys writes key/value pairs into a syncphony.txt, preserving its
// comments and (if the file has them) its BOM and CRLF line endings — that
// file stays the single source of truth, hand-editable on any computer.
// The first active `key=` line is updated, later active duplicates are
// commented out; if the key is only present commented out, the active line
// replaces the last commented one; otherwise it is appended.
func SetKeys(path string, pairs ...KV) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	bom := ""
	content := data
	if len(content) >= 3 && content[0] == 0xef && content[1] == 0xbb && content[2] == 0xbf {
		bom = "\xef\xbb\xbf"
		content = content[3:]
	}
	text := string(content)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	for _, kv := range pairs {
		lines = setKeyInLines(lines, kv.Key, kv.Value)
	}

	out := bom + strings.Join(lines, eol)
	if len(lines) > 0 {
		out += eol
	}
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, ".syncphony.txt.tmp")
	if err := writeFileSync(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// keyLine reports whether a trimmed, case-folded line assigns key: either
// "key" exactly (a bare key) or "key=...".
func keyLine(line, key string) bool {
	name, _, ok := strings.Cut(line, "=")
	if !ok {
		return strings.TrimSpace(line) == key
	}
	return strings.TrimSpace(name) == key
}

func setKeyInLines(lines []string, key, value string) []string {
	activeLine := key + "=" + value
	firstActive := -1
	lastCommented := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			inner := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(trimmed), "#"))
			if keyLine(strings.ToLower(inner), key) {
				lastCommented = i
			}
			continue
		}
		if keyLine(strings.ToLower(trimmed), key) {
			if strings.Contains(trimmed, "=") {
				if firstActive == -1 {
					firstActive = i
					lines[i] = activeLine
				} else {
					lines[i] = "# " + trimmed // later duplicates: comment out
				}
			}
			// A bare `key` line with no `=` is an unrecognized line for the
			// parser; leave it alone and treat the key as absent.
		}
	}
	if firstActive != -1 {
		return lines
	}
	if lastCommented != -1 {
		lines[lastCommented] = activeLine
		return lines
	}
	return append(lines, "", activeLine)
}

// TargetURL builds the /tv URL the kiosk opens on the server, with the box
// version and (when set) the box name, properly encoded (ADR 0016: the box
// opens <server>/tv?box=<version>&name=<name>).
func TargetURL(serverURL, version, name string) string {
	u := strings.TrimSuffix(serverURL, "/")
	u = strings.TrimSuffix(u, "/tv") + "/tv"
	q := "box=" + urlEscape(version)
	if name != "" {
		q += "&name=" + urlEscape(name)
	}
	return u + "?" + q
}

func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// SyncHosts points the Debian 127.0.1.1 line of a hosts file at
// hostname: it replaces the existing line (the image's old name) or
// appends one. Everything else in the file is kept as it was.
func SyncHosts(path, hostname string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	want := "127.0.1.1\t" + hostname
	var out []string
	found := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "127.0.1.1" {
			if !found {
				out = append(out, want)
				found = true
			}
			continue
		}
		if line != "" || len(out) > 0 {
			out = append(out, line)
		}
	}
	if !found {
		out = append(out, want)
	}
	content := strings.Join(out, "\n") + "\n"
	if content == string(data) {
		return nil
	}
	tmp := path + ".tmp"
	if err := writeFileSync(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
