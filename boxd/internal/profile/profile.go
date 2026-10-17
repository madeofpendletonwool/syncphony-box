// Package profile owns the Chromium profile directory — where the box's
// pairing state lives (MAD-804, ADR 0006): the server's syncphony_display
// cookie (HttpOnly, written by Chromium into Default/Cookies) and the
// speaker localStorage flag (Default/Local Storage). Two things make that
// state survive reboots, power cuts and crashes, and both live here or are
// triggered from boxd's bridge:
//
//   - Prep clears Chromium's "exited uncleanly" bookkeeping and its stale
//     profile lock before every start, so a power cut or a rename never
//     leaves a restore prompt or a "profile in use" error on the screen.
//   - Reset wipes the whole profile, so the box shows a fresh pairing code
//     (when it moves to another room or server).
//
// The path itself is defined once in config.DefaultProfileDir (SB_PROFILE
// overrides it for dev and tests) and reaches the kiosk wrapper through
// config.env.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/madeofpendletonwool/syncphony-box/boxd/internal/config"
)

// Path resolves the Chromium profile directory: the SB_PROFILE override,
// else the one box default.
func Path() string {
	if p := os.Getenv("SB_PROFILE"); p != "" {
		return p
	}
	return config.DefaultProfileDir
}

// singletonFiles are Chromium's profile lock: SingletonLock is a symlink
// to "<hostname>-<pid>", the others belong to it. Chromium removes them
// on a clean exit; a kill (cage stopping under it, a power cut) leaves
// them behind. Chromium takes over a lock whose pid is dead only when the
// hostname matches, so after a rename (setup's name=, hostnamectl) it
// refuses the profile with "in use by another Chromium process on another
// computer" — on a TV with nobody there to click through it.
var singletonFiles = []string{"SingletonLock", "SingletonSocket", "SingletonCookie"}

// Prep readies the profile for a start. It removes the stale profile lock
// (singletonFiles) — safe because the kiosk wrapper calls Prep just before
// it execs the box's only Chromium. Then it clears the unclean-exit state
// Chromium records, so a power cut never turns into a "restore pages?"
// prompt on the TV. It edits in place, preserving every other key:
//
//   - <dir>/Default/Preferences: browser.exit_type = "Normal"
//   - <dir>/Local State:         browser.exited_cleanly = true
//
// A fresh profile (files missing) is fine and left alone: nothing to clear.
// A file that is not valid JSON is reported as an error and left untouched
// — the caller logs it and starts Chromium anyway, exactly as it would
// have without this call. The lock goes first, so a bad JSON file never
// keeps it.
func Prep(dir string) error {
	if dir == "" {
		return errors.New("profile: empty directory")
	}
	for _, name := range singletonFiles {
		// os.Remove takes the symlink itself, dangling or not.
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	prefs := filepath.Join(dir, "Default", "Preferences")
	if _, err := os.Stat(prefs); err == nil {
		if err := setJSONKeys(prefs, map[string]any{"exit_type": "Normal"}, "browser"); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	localState := filepath.Join(dir, "Local State")
	if _, err := os.Stat(localState); err == nil {
		if err := setJSONKeys(localState, map[string]any{"exited_cleanly": true}, "browser"); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// setJSONKeys reads a Chromium JSON file (a map of sections), sets keys
// inside the given section, and writes it back atomically. Unknown sections
// are created; every other value is preserved (key order is not — Chromium
// rewrites these files itself and doesn't care).
func setJSONKeys(path string, keys map[string]any, section string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("profile: %s is not valid JSON: %w", path, err)
	}
	obj, ok := root[section].(map[string]any)
	if !ok {
		obj = map[string]any{}
		root[section] = obj
	}
	for k, v := range keys {
		obj[k] = v
	}
	out, err := json.Marshal(root)
	if err != nil {
		return err
	}
	tmp := path + ".boxd-tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Reset removes the Chromium profile entirely, so the next start is a
// fresh browser and the box shows a new pairing code. The kiosk wrapper
// recreates the directory (as the kiosk user) on start, so Reset does not
// recreate it itself — that keeps ownership right whoever runs this.
// The removal is synced to disk before returning, so a power pull right
// after a reset cannot resurrect the old pairing.
func Reset(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) == "/" {
		return fmt.Errorf("profile: refusing to reset %q", dir)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil // already gone: nothing to wipe
	} else if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return Sync()
}

// Sync flushes the system's dirty pages to disk (sync(2)). It is what
// boxd's bridge calls in a short burst after a paired/unpaired (and,
// rate-limited, playing) event: Chromium commits cookies and localStorage
// lazily, so a power cut seconds after pairing can otherwise lose them.
func Sync() error {
	syscall.Sync()
	return nil
}
