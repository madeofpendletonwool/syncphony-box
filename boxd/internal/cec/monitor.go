package cec

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// The monitor side. `cec-ctl -d /dev/cecN --monitor` prints every message
// the adapter sees, one message per line followed by tab-indented operand
// lines (v4l-utils' cec-log format, stable across the versions Debian
// ships):
//
//	Transmitted by Playback 1 (8 to 0): IMAGE_VIEW_ON (0x04)
//	Received from TV (0 to 8): USER_CONTROL_PRESSED (0x44):
//		ui-cmd: select (0x00)
//	Received from TV (0 to 15): ACTIVE_SOURCE (0x82):
//		phys-addr: 1.0.0.0
//
// The parser keeps the pending opcode from a header line and completes it
// from the operand line that follows, so both the exact spacing and any
// future single-line variant still parse.

type eventKind int

const (
	evKeyPress eventKind = iota
	evKeyRelease
	evActiveSource
)

type monitorEvent struct {
	kind     eventKind
	ui       byte   // for press/release: the CEC UI command code
	pa       string // for active source: the physical address
	received bool   // true when someone else sent it, false when we did
}

type monitorParser struct {
	pending  string // the opcode whose operands are expected next
	received bool   // ...and whether it was received or transmitted
}

var (
	uiCmdRE = regexp.MustCompile(`ui-cmd:.*\(0x([0-9a-fA-F]{2})\)`)
	monPaRE = regexp.MustCompile(`phys-addr:\s*([0-9a-f]+(?:\.[0-9a-f]+){3})`)
)

// line feeds one monitor line and returns the events it completed (at
// most one per line in practice; the slice keeps the signature stable).
func (p *monitorParser) line(s string) []monitorEvent {
	if strings.HasPrefix(s, "\t") || strings.HasPrefix(s, " ") {
		defer func() { p.pending = "" }()
		switch p.pending {
		case "USER_CONTROL_PRESSED", "USER_RELEASED":
			if m := uiCmdRE.FindStringSubmatch(s); m != nil {
				if ui, ok := parseUICmdHex(m[1]); ok {
					kind := evKeyPress
					if p.pending == "USER_RELEASED" {
						kind = evKeyRelease
					}
					return []monitorEvent{{kind: kind, ui: ui}}
				}
			}
		case "ACTIVE_SOURCE":
			if m := monPaRE.FindStringSubmatch(s); m != nil {
				return []monitorEvent{{kind: evActiveSource, pa: m[1], received: p.received}}
			}
		}
		return nil
	}
	switch {
	case strings.Contains(s, "USER_CONTROL_PRESSED"):
		p.pending = "USER_CONTROL_PRESSED"
	case strings.Contains(s, "USER_RELEASED"):
		p.pending = "USER_RELEASED"
	case strings.Contains(s, "ACTIVE_SOURCE"):
		p.pending = "ACTIVE_SOURCE"
		p.received = strings.HasPrefix(s, "Received")
	}
	return nil
}

// readLines calls fn for every line of rc until EOF, guarding against a
// monitor that never ends its last line.
func readLines(rc io.Reader, fn func(string)) {
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
}

// runCecCtl runs cec-ctl once for an adapter and returns its stdout.
// Bounded, so a wedged CEC bus can't hang boxd's callers.
func runCecCtl(dev string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	full := append([]string{"-d", dev}, args...)
	out, err := exec.CommandContext(ctx, "cec-ctl", full...).Output()
	return string(out), err
}

// realMonitor spawns `cec-ctl -d <dev> --monitor` and returns its stdout
// plus a stop function that kills the process.
func realMonitor(dev string) (io.ReadCloser, func(), error) {
	cmd := exec.Command("cec-ctl", "-d", dev, "--monitor")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait() //nolint:errcheck — the stop path reports what it needs
		close(done)
	}()
	stop := func() {
		cmd.Process.Kill() //nolint:errcheck — best effort; EOF follows
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return stdout, stop, nil
}
