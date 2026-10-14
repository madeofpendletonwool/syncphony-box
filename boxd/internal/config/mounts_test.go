package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMounts writes a /proc/mounts-shaped file and points SB_MOUNTS at it.
func fakeMounts(t *testing.T, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SB_MOUNTS", p)
}

func TestMountOfPicksTheDeepestReadOnlyMount(t *testing.T) {
	fakeMounts(t, `/dev/mmcblk0p2 / ext4 rw,relatime 0 0
/dev/mmcblk0p1 /boot/firmware vfat ro,relatime 0 0
tmpfs /run tmpfs rw 0 0
`)
	m, ok := MountOf("/boot/firmware/syncphony.txt")
	if !ok || m.Point != "/boot/firmware" || !m.RO {
		t.Fatalf("boot file: %+v ok=%v", m, ok)
	}
	m, ok = MountOf("/etc/hostname")
	if !ok || m.Point != "/" || m.RO {
		t.Fatalf("etc file: %+v ok=%v", m, ok)
	}
}

func TestMountOfRootContainsEverything(t *testing.T) {
	fakeMounts(t, "/dev/sda / ext4 ro 0 0\n")
	if _, ok := MountOf("/anywhere/file"); !ok {
		t.Fatal("/ should contain every absolute path")
	}
}

func TestMountOfMissingTable(t *testing.T) {
	t.Setenv("SB_MOUNTS", filepath.Join(t.TempDir(), "nope"))
	if _, ok := MountOf("/boot/firmware/cmdline.txt"); ok {
		t.Fatal("no table means no mount found")
	}
}

func TestMounted(t *testing.T) {
	fakeMounts(t, "/dev/mmcblk0p3 /data ext4 rw,relatime 0 0\n")
	if !Mounted("/data") {
		t.Fatal("/data should read as mounted")
	}
	if Mounted("/boot/firmware") {
		t.Fatal("/boot/firmware is not in the table")
	}
}

func TestWithMountWritableOnReadOnlyMount(t *testing.T) {
	fakeMounts(t, `/dev/mmcblk0p2 / overlay rw,relatime 0 0
/dev/mmcblk0p1 /boot/firmware vfat ro,relatime 0 0
`)
	var calls []string
	run := func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	wrote := false
	err := WithMountWritable("/boot/firmware/syncphony.txt", run, testLogger(), func() error {
		wrote = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("fn never ran")
	}
	want := []string{
		"mount -o remount,rw /boot/firmware",
		"mount -o remount,ro /boot/firmware",
	}
	if strings.Join(calls, ";") != strings.Join(want, ";") {
		t.Fatalf("remount dance = %v, want %v", calls, want)
	}
}

func TestWithMountWritableOnWritableMountSkipsTheDance(t *testing.T) {
	fakeMounts(t, "/dev/mmcblk0p2 / overlay rw,relatime 0 0\n")
	calls := 0
	run := func(name string, args ...string) error {
		calls++
		return nil
	}
	if err := WithMountWritable("/etc/whatever", run, testLogger(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("writable path triggered %d mount calls", calls)
	}
}

func TestWithMountWritableWithNoTableSkipsTheDance(t *testing.T) {
	// The SB_* hooks point at plain files on a dev machine: nothing is
	// mounted, nothing is read-only, so the write just happens. This is
	// what keeps the bats apply tests (and a dev `boxd config apply`)
	// working without a mount table to dance with.
	t.Setenv("SB_MOUNTS", filepath.Join(t.TempDir(), "nope"))
	calls := 0
	run := func(name string, args ...string) error {
		calls++
		return nil
	}
	wrote := false
	err := WithMountWritable("/tmp/cmdline.txt", run, testLogger(), func() error {
		wrote = true
		return nil
	})
	if err != nil || !wrote {
		t.Fatalf("err=%v wrote=%v", err, wrote)
	}
	if calls != 0 {
		t.Fatalf("no-table path triggered %d mount calls", calls)
	}
}

func TestWithMountWritableFailsCleanlyWhenRemountFails(t *testing.T) {
	fakeMounts(t, "/dev/mmcblk0p1 /boot/firmware vfat ro 0 0\n")
	run := func(name string, args ...string) error {
		return os.ErrPermission
	}
	ran := false
	err := WithMountWritable("/boot/firmware/syncphony.txt", run, testLogger(), func() error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("a failed rw remount must surface")
	}
	if ran {
		t.Fatal("fn must not run against a still-read-only filesystem")
	}
}
