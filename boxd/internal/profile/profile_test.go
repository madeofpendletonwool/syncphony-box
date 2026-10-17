package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
)

func writeProfile(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	prefs := `{"browser":{"exit_type":"Crashed","restore_on_startup":4},"session":{"restore_on_startup_migrated":true}}`
	if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(prefs), 0o644); err != nil {
		t.Fatal(err)
	}
	local := `{"user_experience_metrics":{"stability":{"exited_cleanly":false,"session_end_completed":false}},"browser":{"enabled_labs_experiments":["fl@1"]}}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func TestPrepClearsUncleanExitState(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir)
	if err := Prep(dir); err != nil {
		t.Fatal(err)
	}

	prefs := readJSON(t, filepath.Join(dir, "Default", "Preferences"))
	browser, ok := prefs["browser"].(map[string]any)
	if !ok {
		t.Fatalf("no browser section: %v", prefs)
	}
	if browser["exit_type"] != "Normal" {
		t.Fatalf("exit_type = %v, want Normal", browser["exit_type"])
	}
	if browser["restore_on_startup"] != float64(4) {
		t.Fatalf("unrelated key lost: %v", browser)
	}
	if prefs["session"].(map[string]any)["restore_on_startup_migrated"] != true {
		t.Fatal("unrelated section lost")
	}

	local := readJSON(t, filepath.Join(dir, "Local State"))
	lsBrowser, ok := local["browser"].(map[string]any)
	if !ok {
		t.Fatalf("no browser section in Local State: %v", local)
	}
	if lsBrowser["exited_cleanly"] != true {
		t.Fatalf("exited_cleanly = %v, want true", lsBrowser["exited_cleanly"])
	}
	if lsBrowser["enabled_labs_experiments"] == nil {
		t.Fatal("unrelated Local State key lost")
	}
	stability, ok := local["user_experience_metrics"].(map[string]any)["stability"].(map[string]any)
	if !ok || stability["session_end_completed"] != false {
		t.Fatal("unrelated Local State section lost")
	}
}

func TestPrepCreatesBrowserSectionWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(`{"something":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prep(dir); err != nil {
		t.Fatal(err)
	}
	prefs := readJSON(t, filepath.Join(dir, "Default", "Preferences"))
	if prefs["something"] != true {
		t.Fatal("existing content lost")
	}
	browser, ok := prefs["browser"].(map[string]any)
	if !ok || browser["exit_type"] != "Normal" {
		t.Fatalf("browser section: %v", prefs)
	}
}

func TestPrepOnFreshProfileIsANoOp(t *testing.T) {
	dir := t.TempDir() // empty: a profile Chromium has never written
	if err := Prep(dir); err != nil {
		t.Fatalf("fresh profile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Default")); !os.IsNotExist(err) {
		t.Fatal("Prep created files it shouldn't have")
	}
}

func TestPrepLeavesInvalidJSONAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "Default", "Preferences")
	if err := os.WriteFile(bad, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prep(dir); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
	data, _ := os.ReadFile(bad)
	if string(data) != "not json" {
		t.Fatal("invalid file was modified")
	}
}

// writeStaleLock leaves what a killed Chromium does: SingletonLock is a
// dangling symlink to "<hostname>-<pid>", here a hostname the box no
// longer has (setup renamed it).
func writeStaleLock(t *testing.T, dir string) {
	t.Helper()
	if err := os.Symlink("syncphony-box-a1b2-1234", filepath.Join(dir, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/.org.chromium.Chromium.gone/SingletonSocket", filepath.Join(dir, "SingletonSocket")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("1234567890", filepath.Join(dir, "SingletonCookie")); err != nil {
		t.Fatal(err)
	}
}

func TestPrepRemovesStaleProfileLock(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir)
	writeStaleLock(t, dir)
	if err := Prep(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range singletonFiles {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived Prep (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Default", "Preferences")); err != nil {
		t.Fatalf("profile lost with the lock: %v", err)
	}
}

func TestPrepRemovesLockEvenWithInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStaleLock(t, dir)
	if err := Prep(dir); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
	if _, err := os.Lstat(filepath.Join(dir, "SingletonLock")); !os.IsNotExist(err) {
		t.Fatalf("invalid JSON kept the lock (err=%v)", err)
	}
}

func TestPrepRejectsEmptyDir(t *testing.T) {
	if err := Prep(""); err == nil {
		t.Fatal("expected an error for an empty dir")
	}
}

func TestResetWipesTheProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir)
	if err := Reset(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("profile directory still there")
	}
}

func TestResetOnMissingDirIsANoOp(t *testing.T) {
	if err := Reset(filepath.Join(t.TempDir(), "never-existed")); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}

func TestResetRefusesDangerousPaths(t *testing.T) {
	for _, bad := range []string{"", "/", "relative/path", "./chromium"} {
		if err := Reset(bad); err == nil {
			t.Fatalf("Reset(%q) should be refused", bad)
		}
	}
}

func TestPathRespectsOverrideAndDefault(t *testing.T) {
	t.Setenv("SB_PROFILE", "")
	if got := Path(); got != config.DefaultProfileDir {
		t.Fatalf("default path = %q", got)
	}
	t.Setenv("SB_PROFILE", "/tmp/dev-profile")
	if got := Path(); got != "/tmp/dev-profile" {
		t.Fatalf("override = %q", got)
	}
}
