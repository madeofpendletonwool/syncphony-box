package daemon

import (
	"context"
	"errors"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Thermals (MAD-806): the SoC temperature and the firmware's throttling
// flags, sampled once a minute into the journal (one line per sample, so
// `journalctl -u boxd` doubles as a temperature log) and reported through
// /v1/info for the room's Screens list (stage 4). vcgencmd is the
// Raspberry Pi firmware tool (libraspberrypi-bin); where it is missing —
// a dev machine, a test — thermals simply go unreported and everything
// downstream tolerates their absence.
const thermalsEvery = time.Minute

// thermals is one sample. The flags are vcgencmd get_throttled's bitmap:
// four conditions, current in bits 0-3 and the same four again as sticky
// "has occurred" flags in bits 16-19 (never cleared until reboot — the
// classic value a Pi on a weak power supply reports is 0x50000):
//
//	bit 0  under-voltage now        bit 16 under-voltage occurred
//	bit 1  frequency capped now     bit 17 frequency capped occurred
//	bit 2  throttled now            bit 18 throttling occurred
//	bit 3  soft temperature limit   bit 19 soft temp limit occurred
type thermals struct {
	at       time.Time
	tempC    float64
	rawFlags uint64
}

func (t thermals) valid() bool { return !t.at.IsZero() }

// limitedNow: the firmware is holding the SoC back right now — throttled,
// frequency-capped or at the soft temperature limit.
func (t thermals) limitedNow() bool {
	return t.rawFlags&(1<<1|1<<2|1<<3) != 0
}

func (t thermals) underVoltage() bool     { return t.rawFlags&(1<<0) != 0 }
func (t thermals) throttleOccurred() bool { return t.rawFlags&(0xF<<16) != 0 }

// infoFields is what /v1/info (and /status) add once a sample exists.
func (t thermals) infoFields() map[string]any {
	return map[string]any{
		"temperatureC":     math.Round(t.tempC*10) / 10,
		"throttled":        t.limitedNow(),
		"underVoltage":     t.underVoltage(),
		"throttleOccurred": t.throttleOccurred(),
	}
}

// realVcgencmd runs the firmware tool (on the box; the daemon field is
// stubbed in tests). Bounded, so a wedged firmware call can't stall the
// sampling loop forever.
func realVcgencmd(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "vcgencmd", args...).Output()
	return string(out), err
}

// sampleThermalsLoop samples immediately and then once a minute. It
// returns for good where vcgencmd doesn't exist (not a Pi): thermals are
// optional and absent-by-default everywhere they're reported.
func (d *Daemon) sampleThermalsLoop() {
	if !d.sampleThermals() {
		return
	}
	tick := time.NewTicker(thermalsEvery)
	defer tick.Stop()
	for range tick.C {
		d.sampleThermals()
	}
}

// sampleThermals takes one sample, stores it and logs it. It reports
// false only for "there is no vcgencmd here" (stop sampling); a
// transient failure keeps the loop alive and the journal informed.
func (d *Daemon) sampleThermals() bool {
	tempOut, err := d.vcgencmd("measure_temp")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			d.log.Printf("boxd: thermals: no vcgencmd; temperature and throttling go unreported")
			return false
		}
		d.log.Printf("boxd: thermals: %v", err)
		return true
	}
	temp, tempOK := parseTempC(tempOut)
	flags := uint64(0)
	if out, err := d.vcgencmd("get_throttled"); err == nil {
		flags = parseThrottleFlags(out)
	}
	now := time.Now()
	d.mu.Lock()
	d.therm = thermals{at: now, tempC: temp, rawFlags: flags}
	d.mu.Unlock()
	if tempOK {
		d.log.Printf("boxd: thermals temp=%.1f'C throttled=0x%x", temp, flags)
	} else {
		d.log.Printf("boxd: thermals temp=unknown throttled=0x%x", flags)
	}
	return true
}

// parseTempC reads vcgencmd measure_temp's "temp=52.3'C" into a float.
func parseTempC(out string) (float64, bool) {
	s := strings.TrimSpace(out)
	s = strings.TrimPrefix(s, "temp=")
	s = strings.TrimSuffix(s, "'C")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseThrottleFlags reads vcgencmd get_throttled's "throttled=0x50001"
// into the bitmap; anything unparsable is no flags.
func parseThrottleFlags(out string) uint64 {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "throttled="))
	f, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return 0
	}
	return f
}
