package config

import (
	"log"
	"os"
	"strings"
	"syscall"
)

// The mounts table boxd consults: which filesystem a path lives on, and
// whether that filesystem is read-only. On the box this decides two things
// (MAD-805, ADR 0007):
//
//   - writes to /boot/firmware (syncphony.txt, cmdline.txt) sit on a
//     read-only vfat mount and need the remount dance of WithMountWritable;
//   - /data being mounted is what makes the box's storage (and with it the
//     pairing) healthy — see daemon.StorageHealthy.
//
// SB_MOUNTS points the parser at a fake table for tests, in the spirit of
// the other SB_* hooks.
const defaultMountsPath = "/proc/mounts"

// mountsPath resolves the mounts table to read.
func mountsPath() string {
	if p := os.Getenv("SB_MOUNTS"); p != "" {
		return p
	}
	return defaultMountsPath
}

// Mount is one entry of the mounts table that matters here.
type Mount struct {
	Point string // mount point, octal escapes left as-is (no box path has them)
	RO    bool   // mounted read-only
}

// ReadMounts parses the mounts table; a missing or unreadable table yields
// nil (the callers then behave as "not on a read-only mount").
func ReadMounts() []Mount {
	data, err := os.ReadFile(mountsPath())
	if err != nil {
		return nil
	}
	var mounts []Mount
	for _, line := range strings.Split(string(data), "\n") {
		// Format: device mountpoint fstype options ...
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ro := false
		for _, opt := range strings.Split(fields[3], ",") {
			if opt == "ro" {
				ro = true
			}
		}
		mounts = append(mounts, Mount{Point: fields[1], RO: ro})
	}
	return mounts
}

// MountOf returns the deepest mount whose point contains path — the
// filesystem the path lives on. ok is false when no mount contains it (a
// plain file on a dev machine, say).
func MountOf(path string) (m Mount, ok bool) {
	bestLen := -1
	for _, mount := range ReadMounts() {
		if mount.Point == "/" || mount.Point == path || strings.HasPrefix(path, mount.Point+"/") {
			if len(mount.Point) > bestLen {
				m, ok, bestLen = mount, true, len(mount.Point)
			}
		}
	}
	return m, ok
}

// Mounted reports whether the given mount point carries a filesystem.
func Mounted(point string) bool {
	for _, mount := range ReadMounts() {
		if mount.Point == point {
			return true
		}
	}
	return false
}

// WithMountWritable runs fn while the filesystem holding path is writable,
// then puts it back: remount rw, run fn, sync, remount ro. The box keeps
// /boot/firmware read-only (ADR 0007) so a power pull can never catch the
// FAT partition half-written; the only writes that ever hit it go through
// this dance.
//
// A path on an already-writable filesystem — or on no mount at all (the
// SB_* test hooks point at plain files) — runs fn directly, so dev
// machines and tests never perform (or fail) a remount.
//
// Fail-safe bias: a fn error is returned even if the ro remount also fails
// (the write may have landed), and a failed remount back to ro is logged
// but never discarded silently — the box keeps running either way.
func WithMountWritable(path string, run RunCommand, log *log.Logger, fn func() error) error {
	mount, ok := MountOf(path)
	if !ok || !mount.RO {
		return fn()
	}
	if err := run("mount", "-o", "remount,rw", mount.Point); err != nil {
		return err
	}
	err := fn()
	syscall.Sync()
	if roErr := run("mount", "-o", "remount,ro", mount.Point); roErr != nil {
		log.Printf("boxd: cannot remount %s read-only after writing: %v (leaving it writable)", mount.Point, roErr)
	}
	return err
}
