// Package update is the box's nightly update (MAD-810, ADR 0012):
// Debian security updates and new versions of the syncphony-box package
// (boxd, the kiosk, the units — everything that isn't the web app, which
// the Syncphony server serves itself), installed on the REAL root
// filesystem through `syncphony-box chroot` — runtime writes under / land
// in the overlay's tmpfs and would vanish at the next boot (ADR 0007).
//
// The policy, in order:
//
//   - auto_update=off in syncphony.txt skips the nightly run (a manual
//     `sudo syncphony-box update` still runs).
//   - While the room is playing (the bridge's last event, with a live
//     heartbeat), the nightly run skips: tomorrow is another night.
//   - apt-get update, then apt-get upgrade --with-new-pkgs (a kernel or a
//     new syncphony-box dependency arrives as a new package).
//   - The result — updated, current or failed, when, the installed
//     syncphony-box version — goes to /data for /v1/info (the room's
//     Screens list shows it, MAD-811).
//   - Only if something changed, and only once the room is idle, the box
//     reboots into it. A failed update never reboots: the old system is
//     still the running one, and the next night retries.
package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultStatePath is the last run's result, on /data so it survives
	// the reboot the run itself causes. SB_UPDATE_STATE overrides it.
	DefaultStatePath = "/data/syncphony-box/update.json"

	// The bridge's local status (served by boxd on loopback): "playing".
	statusURL = "http://127.0.0.1:8099/status"

	// minFreeBytes on the root partition before an upgrade starts. The
	// root never grows (ADR 0007); pi-gen leaves 20% + 200 MB of
	// headroom, and a kernel upgrade unpacked half-way is worse than one
	// not started.
	minFreeBytes = 150 << 20

	// The idle wait before the reboot: poll every minute, give up after
	// IdleWait (the next boot picks the update up anyway).
	idlePoll        = time.Minute
	DefaultIdleWait = 4 * time.Hour

	// The package this repo builds; its version is the box's version.
	PackageName = "syncphony-box"

	// SourceList is the box's own apt repository, shipped by the package.
	// It is handed to apt only here (as Dir::Etc::SourceList — the stock
	// sources.list is empty on Pi OS; Debian and Raspberry Pi live in
	// sources.list.d and stay), never installed into /etc/apt: the image
	// build's own apt runs must not see it (ADR 0012).
	SourceList = "/usr/share/syncphony-box/apt/syncphony-box.list"

	lockPath = "/run/syncphony-update.lock"

	// scheduledHour is syncphony-update.timer's hour (04:00 plus up to
	// 10 minutes of spread). A timer run outside it is a clock jump, not
	// the schedule: a box with no RTC (every Pi, and a PC with a flat
	// battery) boots with an old clock, systemd plans the next 04:00 from
	// it, and when NTP corrects the clock that 04:00 is in the past and
	// the timer fires at once, mid-setup, in the middle of the day.
	scheduledHour = 4
)

// Results recorded in the state file.
const (
	ResultUpdated = "updated" // something was installed; the box reboots into it
	ResultCurrent = "current" // nothing to do
	ResultFailed  = "failed"  // see Error; the box keeps running the old system
)

// State is the last update run, as /v1/info reports it.
type State struct {
	At      time.Time `json:"at"`
	Result  string    `json:"result"`
	Error   string    `json:"error,omitempty"`
	Version string    `json:"version,omitempty"` // the installed syncphony-box package after the run
	Changed []string  `json:"changed,omitempty"` // "name old -> new"
	Manual  bool      `json:"manual,omitempty"`
}

// StatePath is where the state file lives (the SB_UPDATE_STATE hook for
// dev and tests, like the other SB_* paths).
func StatePath() string {
	if p := os.Getenv("SB_UPDATE_STATE"); p != "" {
		return p
	}
	return DefaultStatePath
}

// ReadState reads the last run's result; false when there is none (a
// box that has never updated) or it can't be read.
func ReadState(path string) (State, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, false
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil || s.At.IsZero() {
		return State{}, false
	}
	return s, true
}

// writeState replaces the state file atomically and durably: the reboot
// that follows a successful run must not lose it.
func writeState(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Env is everything Run touches outside itself — real on a box, fakes in
// the tests.
type Env struct {
	Log *log.Logger
	// Run executes a command, returning its stdout and stderr separately
	// (dpkg-query's stdout is parsed; apt's output is logged).
	Run func(name string, args ...string) (stdout, stderr string, err error)
	// Playing reports whether the room is playing right now.
	Playing func() (bool, error)
	// OverlayActive reports whether / is the read-only overlay (then the
	// real root is reached through `syncphony-box chroot`).
	OverlayActive func() bool
	// HasSourceList reports whether the package shipped the repo (a dev
	// build without the signing key doesn't); then only Debian and
	// Raspberry Pi updates install.
	HasSourceList func() bool
	// FreeBytes is the free space on the real root filesystem.
	FreeBytes func(overlay bool) (uint64, error)
	Sleep     func(time.Duration)
	Now       func() time.Time
	StatePath string
}

// Options for one run.
type Options struct {
	Manual     bool          // sudo syncphony-box update: ignores auto_update=off and playback
	NoReboot   bool          // install, but leave the reboot to the person who asked
	AutoUpdate string        // auto_update= from syncphony.txt ("on"/"off")
	IdleWait   time.Duration // how long to wait for an idle room before rebooting
}

// RealEnv is the box: real commands, the bridge on loopback, statfs.
func RealEnv(logger *log.Logger) Env {
	return Env{
		Log:           logger,
		Run:           realRun,
		Playing:       realPlaying,
		OverlayActive: realOverlayActive,
		HasSourceList: func() bool { _, err := os.Stat(SourceList); return err == nil },
		FreeBytes:     realFreeBytes,
		Sleep:         time.Sleep,
		Now:           time.Now,
		StatePath:     StatePath(),
	}
}

// Lock takes the run lock so the timer and a manual run never overlap.
// The returned func releases it.
func Lock() (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another update is already running")
	}
	return func() { f.Close() }, nil
}

// Skipped is returned (with no state written) when the policy says not
// tonight: auto_update=off, or the room is playing.
var Skipped = errors.New("update skipped")

// Run performs one update. It returns the recorded state; Skipped when
// nothing was attempted; and a non-nil error for a failed run (the state
// records it too).
func Run(env Env, opt Options) (State, error) {
	lg := env.Log
	if !opt.Manual && opt.AutoUpdate == "off" {
		lg.Printf("update: auto_update=off in syncphony.txt; skipping (sudo syncphony-box update still works)")
		return State{}, Skipped
	}
	if now := env.Now(); !opt.Manual && now.Hour() != scheduledHour {
		lg.Printf("update: started at %s, outside the %02d:00 window (the clock jumped); skipping until tonight", now.Format("15:04"), scheduledHour)
		return State{}, Skipped
	}
	if playing, err := env.Playing(); err != nil {
		// boxd down means no page and no music; nothing to protect.
		lg.Printf("update: cannot ask boxd whether the room is playing (%v); treating it as idle", err)
	} else if playing {
		if !opt.Manual {
			lg.Printf("update: the room is playing; skipping tonight's update")
			return State{}, Skipped
		}
		lg.Printf("update: the room is playing; updating anyway (asked by hand), the reboot waits for a pause")
	}

	overlay := env.OverlayActive()
	var prefix []string
	if overlay {
		prefix = []string{"/usr/bin/syncphony-box", "chroot"}
	}
	onRoot := func(name string, args ...string) (string, string, error) {
		full := append(append(append([]string{}, prefix...), name), args...)
		return env.Run(full[0], full[1:]...)
	}

	state := State{At: env.Now().UTC(), Manual: opt.Manual}
	fail := func(format string, args ...any) (State, error) {
		state.Result = ResultFailed
		state.Error = fmt.Sprintf(format, args...)
		lg.Printf("update: failed: %s", state.Error)
		if err := writeState(env.StatePath, state); err != nil {
			lg.Printf("update: cannot record the result: %v", err)
		}
		return state, errors.New(state.Error)
	}

	if free, err := env.FreeBytes(overlay); err == nil && free < minFreeBytes {
		return fail("not enough space on the root partition (%d MB free, need %d MB)", free>>20, minFreeBytes>>20)
	}

	before, err := installed(onRoot)
	if err != nil {
		return fail("cannot list installed packages: %v", err)
	}
	state.Version = before[PackageName]

	apt := []string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-q", "-y",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	if env.HasSourceList != nil && env.HasSourceList() {
		apt = append(apt, "-o", "Dir::Etc::SourceList="+SourceList)
	} else {
		lg.Printf("update: no %s (a build without the repo key): Debian and Raspberry Pi updates only", SourceList)
	}
	lg.Printf("update: apt-get update")
	// --error-on=any: a repo that can't be fetched or whose signature
	// doesn't verify is only a warning to plain apt-get update (it keeps
	// the old lists). Here it's a failure — the Screens list should say
	// "update failed", not "current".
	if out, errOut, err := onRoot("env", append(apt, "--error-on=any", "update")...); err != nil {
		logOutput(lg, out, errOut)
		return fail("apt-get update: %v: %s", err, tail(errOut, out))
	}
	lg.Printf("update: apt-get upgrade")
	out, errOut, upErr := onRoot("env", append(apt, "--with-new-pkgs", "upgrade")...)
	logOutput(lg, out, errOut)
	if _, _, err := onRoot("apt-get", "clean"); err != nil {
		lg.Printf("update: apt-get clean: %v", err)
	}

	after, err := installed(onRoot)
	if err != nil {
		return fail("cannot list installed packages after the upgrade: %v", err)
	}
	state.Version = after[PackageName]
	state.Changed = diff(before, after)
	if upErr != nil {
		// Packages may be half-configured; the running system is still
		// the old one (the overlay's lower layer changed underneath, but
		// nothing reloads it until a reboot). Don't reboot into it: the
		// next night retries, and docs/releasing.md says what to do if it
		// keeps failing (reflash, re-pair).
		return fail("apt-get upgrade: %v: %s", upErr, tail(errOut, out))
	}

	if len(state.Changed) == 0 {
		state.Result = ResultCurrent
		lg.Printf("update: already current (%s %s)", PackageName, orNone(state.Version))
	} else {
		state.Result = ResultUpdated
		lg.Printf("update: %d package(s) changed: %s", len(state.Changed), strings.Join(state.Changed, ", "))
	}
	if err := writeState(env.StatePath, state); err != nil {
		lg.Printf("update: cannot record the result: %v", err)
	}

	if state.Result == ResultUpdated {
		if opt.NoReboot {
			lg.Printf("update: reboot to finish (--no-reboot)")
		} else {
			rebootWhenIdle(env, opt)
		}
	}
	return state, nil
}

// rebootWhenIdle reboots as soon as the room isn't playing, polling for
// up to opt.IdleWait. If the party outlasts it, the update simply lands
// at the next boot.
func rebootWhenIdle(env Env, opt Options) {
	wait := opt.IdleWait
	if wait == 0 {
		wait = DefaultIdleWait
	}
	for waited := time.Duration(0); ; waited += idlePoll {
		playing, err := env.Playing()
		if err != nil || !playing {
			env.Log.Printf("update: rebooting into the update")
			if _, errOut, err := env.Run("systemctl", "reboot"); err != nil {
				env.Log.Printf("update: reboot failed: %v %s", err, errOut)
			}
			return
		}
		if waited >= wait {
			env.Log.Printf("update: the room kept playing for %s; the update applies at the next boot", wait)
			return
		}
		env.Sleep(idlePoll)
	}
}

// installed lists the installed packages and their versions on the real
// root.
func installed(onRoot func(string, ...string) (string, string, error)) (map[string]string, error) {
	out, errOut, err := onRoot("dpkg-query", "-W", "-f=${db:Status-Abbrev}|${Package}|${Version}\n")
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(errOut))
	}
	return parseInstalled(out), nil
}

func parseInstalled(out string) map[string]string {
	pkgs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 || !strings.HasPrefix(parts[0], "ii") {
			continue
		}
		pkgs[parts[1]] = parts[2]
	}
	return pkgs
}

// diff lists what changed between two package snapshots, sorted.
func diff(before, after map[string]string) []string {
	var changed []string
	for name, v := range after {
		switch old, ok := before[name]; {
		case !ok:
			changed = append(changed, fmt.Sprintf("%s (new) -> %s", name, v))
		case old != v:
			changed = append(changed, fmt.Sprintf("%s %s -> %s", name, old, v))
		}
	}
	for name, old := range before {
		if _, ok := after[name]; !ok {
			changed = append(changed, fmt.Sprintf("%s %s -> (removed)", name, old))
		}
	}
	sort.Strings(changed)
	return changed
}

func logOutput(lg *log.Logger, out, errOut string) {
	for _, s := range []string{out, errOut} {
		for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
			if line != "" {
				lg.Printf("update: | %s", line)
			}
		}
	}
}

// tail is what the Screens list's "update failed" should say: apt's own
// E:/W: lines when there are any (they name the repo and the reason),
// else the last few lines of output.
func tail(outs ...string) string {
	var lines, apt []string
	for _, s := range outs {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lines = append(lines, line)
			if strings.HasPrefix(line, "E: ") || strings.HasPrefix(line, "W: ") {
				apt = append(apt, line)
			}
		}
	}
	if len(apt) > 0 {
		lines = apt
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	msg := strings.Join(lines, " / ")
	if len(msg) > 400 {
		msg = msg[:400]
	}
	return msg
}

func orNone(s string) string {
	if s == "" {
		return "not installed"
	}
	return s
}

func realRun(name string, args ...string) (string, string, error) {
	var out, errOut bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.String(), errOut.String(), err
}

var statusClient = &http.Client{Timeout: 5 * time.Second}

func realPlaying() (bool, error) {
	resp, err := statusClient.Get(statusURL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var st struct {
		Playing bool `json:"playing"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return false, err
	}
	return st.Playing, nil
}

// realOverlayActive: the box's overlay is up — / is an overlay (the last
// /proc/mounts entry for / is what / is) AND the initramfs left the real
// root at /media/root-ro. An overlay root without that (a container) is
// somebody else's and is written directly.
func realOverlayActive() bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	return overlayFromMounts(string(data))
}

func overlayFromMounts(mounts string) bool {
	fstype, realRoot := "", false
	for _, line := range strings.Split(mounts, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		switch f[1] {
		case "/":
			fstype = f[2]
		case "/media/root-ro":
			realRoot = true
		}
	}
	return fstype == "overlay" && realRoot
}

// realFreeBytes: under the overlay the real root is the read-only mount
// the initramfs leaves at /media/root-ro (ADR 0007).
func realFreeBytes(overlay bool) (uint64, error) {
	path := "/"
	if overlay {
		path = "/media/root-ro"
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
