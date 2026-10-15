package cec

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The injected keyboard. uinput is the kernel's "make a keyboard" API:
// create a virtual device, tell it which keys it has, write input_event
// structs. cage (like any Wayland compositor) picks the device up through
// libinput and Chromium sees ordinary key events — no shell, no root
// helper, just /dev/uinput (which the udev rule in
// packaging/root/usr/lib/udev/rules.d grants boxd's group).

// From linux/input.h and linux/uinput.h.
const (
	evSyn = 0x00
	evKey = 0x01

	synReport = 0x00

	uiSetEvBit  = 0x40045564 // _IOW('U', 100, int)
	uiSetKeyBit = 0x40045565 // _IOW('U', 101, int)
	uiDevCreate = 0x5501     // _IO('U', 1)
)

// inputEvent is linux input_event for a 64-bit kernel: a timeval (two
// longs — kernel time), then type, code, value. 24 bytes.
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

// uinputUserDev is linux uinput_user_dev: a name, an input_id, and the
// absolute-axis arrays (zeroed for a keyboard). 1116 bytes.
type uinputUserDev struct {
	Name         [80]byte
	Bustype      uint16
	Vendor       uint16
	Product      uint16
	Version      uint16
	FFEffectsMax int32
	Absmax       [64]int32
	Absmin       [64]int32
	Absfuzz      [64]int32
	Absflat      [64]int32
}

// keyDevice is the keyboard remoteKeys injects through; an interface so
// tests can record instead of typing.
type keyDevice interface {
	Press(code uint16) error
	Release(code uint16) error
	Close() error
}

// uinputDev is a real /dev/uinput keyboard. SB_UINPUT points it at
// another node in dev and tests, mirroring the SB_* hooks.
type uinputDev struct {
	f *os.File
}

// theKeys is every key mapKey can produce, plus the ones /tv might grow;
// registering them all up front keeps the device from surprising libinput
// with a new keycode later.
var theKeys = []uint16{keyEsc, keyEnter, keyUp, keyDown, keyLeft, keyRight, keyPlayPause}

func openUinput() (keyDevice, error) {
	path := "/dev/uinput"
	if p := os.Getenv("SB_UINPUT"); p != "" {
		path = p
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	d := &uinputDev{f: os.NewFile(uintptr(fd), path)}
	if err := d.ioctl(uiSetEvBit, int64(evKey)); err != nil {
		d.f.Close()
		return nil, err
	}
	for _, k := range theKeys {
		if err := d.ioctl(uiSetKeyBit, int64(k)); err != nil {
			d.f.Close()
			return nil, err
		}
	}
	var u uinputUserDev
	copy(u.Name[:], "Syncphony Box remote")
	u.Bustype = 0x06 // BUS_VIRTUAL
	u.Vendor, u.Product, u.Version = 0x0001, 0x0001, 0x0001
	if _, err := d.f.Write(unsafeSlice(u)); err != nil {
		d.f.Close()
		return nil, fmt.Errorf("write uinput_user_dev: %w", err)
	}
	if err := d.ioctlRaw(uiDevCreate); err != nil {
		d.f.Close()
		return nil, err
	}
	return d, nil
}

func (d *uinputDev) Press(code uint16) error   { return d.event(code, 1) }
func (d *uinputDev) Release(code uint16) error { return d.event(code, 0) }

func (d *uinputDev) Close() error { return d.f.Close() }

func (d *uinputDev) event(code uint16, value int32) error {
	ev := inputEvent{Type: evKey, Code: code, Value: value}
	if _, err := d.f.Write(unsafeSlice(ev)); err != nil {
		return err
	}
	syn := inputEvent{Type: evSyn, Code: synReport}
	_, err := d.f.Write(unsafeSlice(syn))
	return err
}

func (d *uinputDev) ioctl(req uint, arg int64) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), uintptr(req), uintptr(arg))
	if errno != 0 {
		return fmt.Errorf("ioctl 0x%x: %v", req, errno)
	}
	return nil
}

func (d *uinputDev) ioctlRaw(req uint) error {
	return d.ioctl(req, 0)
}

// unsafeSlice renders any of the fixed structs above as the bytes the
// kernel expects. The two types have no padding on any Go platform the
// box builds for (arm64/amd64: both are naturally aligned).
func unsafeSlice[T any](v T) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&v)), unsafe.Sizeof(v))
}
