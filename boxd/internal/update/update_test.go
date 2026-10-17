package update

import (
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBox scripts the commands an update runs: the dpkg-query snapshots
// (before, then after) and apt's success or failure.
type fakeBox struct {
	t         *testing.T
	calls     []string
	snapshots []string // dpkg-query outputs, in order
	failOn    string   // a substring of the command line that fails
	playing   []bool   // Playing answers, in order (the last repeats)
	overlay   bool
	free      uint64
	slept     time.Duration
}

func (f *fakeBox) env(statePath string) Env {
	return Env{
		Log: log.New(io.Discard, "", 0),
		Run: func(name string, args ...string) (string, string, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			f.calls = append(f.calls, line)
			if f.failOn != "" && strings.Contains(line, f.failOn) {
				return "", "E: Unable to fetch some archives\n", errors.New("exit status 100")
			}
			if strings.Contains(line, "dpkg-query") {
				if len(f.snapshots) == 0 {
					f.t.Fatalf("unexpected dpkg-query")
				}
				out := f.snapshots[0]
				f.snapshots = f.snapshots[1:]
				return out, "", nil
			}
			return "", "", nil
		},
		Playing: func() (bool, error) {
			if len(f.playing) == 0 {
				return false, nil
			}
			p := f.playing[0]
			if len(f.playing) > 1 {
				f.playing = f.playing[1:]
			}
			return p, nil
		},
		OverlayActive: func() bool { return f.overlay },
		HasSourceList: func() bool { return true },
		FreeBytes:     func(bool) (uint64, error) { return f.free, nil },
		Sleep:         func(d time.Duration) { f.slept += d },
		Now:           func() time.Time { return time.Date(2026, 10, 16, 4, 3, 0, 0, time.UTC) },
		StatePath:     statePath,
	}
}

func (f *fakeBox) ran(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

const (
	snapN  = "ii |syncphony-box|1.2.0\nii |libc6|2.41-6\nrc |old-thing|1.0\n"
	snapN1 = "ii |syncphony-box|1.3.0\nii |libc6|2.41-6+deb13u1\nii |linux-image-6.12.50|1\n"
)

func newBox(t *testing.T, snaps ...string) *fakeBox {
	return &fakeBox{t: t, snapshots: snaps, overlay: true, free: 1 << 30}
}

func TestUpdateInstallsRecordsAndRebootsWhenIdle(t *testing.T) {
	box := newBox(t, snapN, snapN1)
	path := filepath.Join(t.TempDir(), "update.json")
	st, err := Run(box.env(path), Options{AutoUpdate: "on"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Result != ResultUpdated || st.Version != "1.3.0" {
		t.Fatalf("state %+v", st)
	}
	want := []string{
		"libc6 2.41-6 -> 2.41-6+deb13u1",
		"linux-image-6.12.50 (new) -> 1",
		"syncphony-box 1.2.0 -> 1.3.0",
	}
	if strings.Join(st.Changed, "|") != strings.Join(want, "|") {
		t.Fatalf("changed %q", st.Changed)
	}
	// Everything ran on the real root, through the chroot.
	for _, c := range box.calls {
		if c != "systemctl reboot" && !strings.HasPrefix(c, "/usr/bin/syncphony-box chroot ") {
			t.Errorf("not through the chroot: %s", c)
		}
	}
	if !box.ran("--with-new-pkgs upgrade") || !box.ran("--force-confold") ||
		!box.ran("Dir::Etc::SourceList="+SourceList+" --error-on=any update") {
		t.Errorf("upgrade not as expected: %q", box.calls)
	}
	if !box.ran("systemctl reboot") {
		t.Errorf("no reboot after a changing update")
	}
	saved, ok := ReadState(path)
	if !ok || saved.Result != ResultUpdated || saved.Version != "1.3.0" || !saved.At.Equal(st.At) {
		t.Fatalf("saved %+v ok=%v", saved, ok)
	}
}

func TestNothingChangedMeansNoReboot(t *testing.T) {
	box := newBox(t, snapN, snapN)
	path := filepath.Join(t.TempDir(), "update.json")
	st, err := Run(box.env(path), Options{AutoUpdate: "on"})
	if err != nil || st.Result != ResultCurrent || st.Version != "1.2.0" {
		t.Fatalf("st %+v err %v", st, err)
	}
	if box.ran("reboot") {
		t.Fatal("rebooted with nothing changed")
	}
	if saved, ok := ReadState(path); !ok || saved.Result != ResultCurrent {
		t.Fatalf("saved %+v", saved)
	}
}

func TestPlayingSkipsTheNightlyRun(t *testing.T) {
	box := newBox(t)
	box.playing = []bool{true}
	path := filepath.Join(t.TempDir(), "update.json")
	if _, err := Run(box.env(path), Options{AutoUpdate: "on"}); err != Skipped {
		t.Fatalf("err %v", err)
	}
	if len(box.calls) != 0 {
		t.Fatalf("ran %q while playing", box.calls)
	}
	if _, ok := ReadState(path); ok {
		t.Fatal("a skip must not overwrite the last result")
	}
}

func TestTimerRunOutsideTheWindowSkipsUnlessManual(t *testing.T) {
	// The clock-jump case: NTP moved the clock past the planned 04:00 and
	// the timer fired at once, in the afternoon.
	box := newBox(t)
	env := box.env(filepath.Join(t.TempDir(), "u.json"))
	env.Now = func() time.Time { return time.Date(2026, 10, 17, 17, 6, 0, 0, time.UTC) }
	if _, err := Run(env, Options{AutoUpdate: "on"}); err != Skipped {
		t.Fatalf("err %v", err)
	}
	if len(box.calls) != 0 {
		t.Fatalf("ran %q outside the window", box.calls)
	}

	box = newBox(t, snapN, snapN)
	env = box.env(filepath.Join(t.TempDir(), "u.json"))
	env.Now = func() time.Time { return time.Date(2026, 10, 17, 17, 6, 0, 0, time.UTC) }
	if st, err := Run(env, Options{AutoUpdate: "on", Manual: true}); err != nil || st.Result != ResultCurrent {
		t.Fatalf("manual run: st %+v err %v", st, err)
	}
}

func TestAutoUpdateOffSkipsUnlessManual(t *testing.T) {
	box := newBox(t)
	if _, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{AutoUpdate: "off"}); err != Skipped {
		t.Fatalf("err %v", err)
	}
	box = newBox(t, snapN, snapN)
	st, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{AutoUpdate: "off", Manual: true})
	if err != nil || st.Result != ResultCurrent || !st.Manual {
		t.Fatalf("manual run: st %+v err %v", st, err)
	}
}

func TestRebootWaitsForTheRoomToGoIdle(t *testing.T) {
	box := newBox(t, snapN, snapN1)
	// Manual run while playing: update anyway, then wait out three more
	// "playing" polls before the reboot.
	box.playing = []bool{true, true, true, true, false}
	_, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{Manual: true, AutoUpdate: "on"})
	if err != nil {
		t.Fatal(err)
	}
	if !box.ran("systemctl reboot") || box.slept != 3*idlePoll {
		t.Fatalf("slept %s, calls %q", box.slept, box.calls)
	}
}

func TestRebootGivesUpAfterTheIdleWait(t *testing.T) {
	box := newBox(t, snapN, snapN1)
	box.playing = []bool{false, true} // idle at the start, then a party
	_, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{AutoUpdate: "on", IdleWait: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if box.ran("reboot") {
		t.Fatal("rebooted during playback")
	}
	if box.slept != 10*time.Minute {
		t.Fatalf("slept %s", box.slept)
	}
}

func TestNoRebootFlag(t *testing.T) {
	box := newBox(t, snapN, snapN1)
	if _, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{AutoUpdate: "on", NoReboot: true}); err != nil {
		t.Fatal(err)
	}
	if box.ran("reboot") {
		t.Fatal("rebooted with --no-reboot")
	}
}

func TestFailedUpgradeRecordsAndNeverReboots(t *testing.T) {
	box := newBox(t, snapN, snapN)
	box.failOn = "upgrade"
	path := filepath.Join(t.TempDir(), "u.json")
	st, err := Run(box.env(path), Options{AutoUpdate: "on"})
	if err == nil || st.Result != ResultFailed {
		t.Fatalf("st %+v err %v", st, err)
	}
	if !strings.Contains(st.Error, "Unable to fetch") {
		t.Fatalf("error should carry apt's reason: %q", st.Error)
	}
	if box.ran("reboot") {
		t.Fatal("rebooted after a failure")
	}
	if saved, ok := ReadState(path); !ok || saved.Result != ResultFailed {
		t.Fatalf("saved %+v", saved)
	}
}

func TestFailedAptUpdate(t *testing.T) {
	box := newBox(t, snapN)
	box.failOn = " update"
	st, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{AutoUpdate: "on"})
	if err == nil || st.Result != ResultFailed || !strings.HasPrefix(st.Error, "apt-get update") {
		t.Fatalf("st %+v err %v", st, err)
	}
	if box.ran("--with-new-pkgs") {
		t.Fatal("upgraded after apt-get update failed")
	}
}

func TestLowDiskFailsBeforeTouchingAnything(t *testing.T) {
	box := newBox(t)
	box.free = 20 << 20
	st, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{AutoUpdate: "on"})
	if err == nil || st.Result != ResultFailed || !strings.Contains(st.Error, "not enough space") {
		t.Fatalf("st %+v err %v", st, err)
	}
	if len(box.calls) != 0 {
		t.Fatalf("ran %q", box.calls)
	}
}

func TestWithoutTheOverlayCommandsRunDirectly(t *testing.T) {
	box := newBox(t, snapN, snapN)
	box.overlay = false
	if _, err := Run(box.env(filepath.Join(t.TempDir(), "u.json")), Options{AutoUpdate: "on"}); err != nil {
		t.Fatal(err)
	}
	if box.ran("chroot") {
		t.Fatalf("used the chroot with the overlay off: %q", box.calls)
	}
}

func TestReadStateMissingOrGarbage(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadState(filepath.Join(dir, "nope.json")); ok {
		t.Fatal("missing file read as a state")
	}
}

func TestOverlayMeansTheBoxOverlay(t *testing.T) {
	box := "/dev/mmcblk0p2 /media/root-ro ext4 ro,relatime 0 0\n" +
		"tmpfs-root /media/root-rw tmpfs rw 0 0\n" +
		"overlayroot / overlay rw,lowerdir=/media/root-ro 0 0\n"
	if !overlayFromMounts(box) {
		t.Fatal("the box's overlay not recognized")
	}
	container := "overlay / overlay rw,lowerdir=/var/lib/docker/... 0 0\n"
	if overlayFromMounts(container) {
		t.Fatal("a container's overlay root taken for the box's")
	}
	if overlayFromMounts("/dev/mmcblk0p2 / ext4 rw 0 0\n") {
		t.Fatal("a plain root taken for an overlay")
	}
}

func TestTailPrefersAptsOwnErrors(t *testing.T) {
	out := "Hit:1 http://deb.debian.org/debian trixie InRelease\nReading package lists...\n"
	errOut := "W: OpenPGP signature verification failed: http://x/apt stable InRelease\nE: The repository is not signed.\n"
	got := tail(errOut, out)
	if got != "W: OpenPGP signature verification failed: http://x/apt stable InRelease / E: The repository is not signed." {
		t.Fatalf("got %q", got)
	}
	if got := tail("", "a\nb\nc\nd\n"); got != "b / c / d" {
		t.Fatalf("got %q", got)
	}
}
