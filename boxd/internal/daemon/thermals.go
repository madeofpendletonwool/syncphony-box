package daemon

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Thermals (MAD-806): the SoC temperature and the firmware's throttling
// flags, sampled once a minute into the journal (one line per sample, so
// `journalctl -u boxd` doubles as a temperature log) and reported through
// /v1/info for the room's Screens list (stage 4). vcgencmd is the
// Raspberry Pi firmware tool (raspi-utils-core). Without it — an x86 box
// (MAD-846) — the temperature comes from the kernel's thermal zones in
// /sys/class/thermal, with no throttling flags (a PC has no equivalent of
// the Pi firmware's bitmap). With neither, thermals simply go unreported
// and everything downstream tolerates their absence.
const thermalsEvery = time.Minute

// defaultThermalDir is where the kernel lists its thermal zones; the
// SB_THERMAL_DIR hook points it at a fixture in tests.
const defaultThermalDir = "/sys/class/thermal"

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
	hasFlags bool // the source reports throttling flags (vcgencmd only)
}

func (t thermals) valid() bool { return !t.at.IsZero() }

// limitedNow: the firmware is holding the SoC back right now — throttled,
// frequency-capped or at the soft temperature limit.
func (t thermals) limitedNow() bool {
	return t.rawFlags&(1<<1|1<<2|1<<3) != 0
}

func (t thermals) underVoltage() bool     { return t.rawFlags&(1<<0) != 0 }
func (t thermals) throttleOccurred() bool { return t.rawFlags&(0xF<<16) != 0 }

// infoFields is what /v1/info (and /status) add once a sample exists. The
// flag fields only come from a source that has them: a PC's thermal zone
// saying nothing about throttling is not "not throttled".
func (t thermals) infoFields() map[string]any {
	m := map[string]any{"temperatureC": math.Round(t.tempC*10) / 10}
	if t.hasFlags {
		m["throttled"] = t.limitedNow()
		m["underVoltage"] = t.underVoltage()
		m["throttleOccurred"] = t.throttleOccurred()
	}
	return m
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
// returns for good where neither vcgencmd nor a thermal zone exists:
// thermals are optional and absent-by-default everywhere they're reported.
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
// false only for "there is nothing to sample here" (stop sampling); a
// transient failure keeps the loop alive and the journal informed.
func (d *Daemon) sampleThermals() bool {
	if d.thermalZone != "" {
		return d.sampleThermalZone()
	}
	tempOut, err := d.vcgencmd("measure_temp")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			zone, ok := pickThermalZone(d.thermalDir)
			if !ok {
				d.log.Printf("boxd: thermals: no vcgencmd and no thermal zone; temperature goes unreported")
				return false
			}
			d.thermalZone = zone
			d.log.Printf("boxd: thermals: no vcgencmd; reading %s (no throttling flags)", zone)
			return d.sampleThermalZone()
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
	d.therm = thermals{at: now, tempC: temp, rawFlags: flags, hasFlags: true}
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

// sampleThermalZone is sampleThermals for a box without vcgencmd: one
// read of the chosen zone's temp file (millidegrees Celsius).
func (d *Daemon) sampleThermalZone() bool {
	temp, err := readZoneTempC(d.thermalZone)
	if err != nil {
		d.log.Printf("boxd: thermals: %v", err)
		return true
	}
	d.mu.Lock()
	d.therm = thermals{at: time.Now(), tempC: temp}
	d.mu.Unlock()
	d.log.Printf("boxd: thermals temp=%.1f'C", temp)
	return true
}

// thermalZonePreference ranks zone types: the CPU package sensor first,
// then the other CPU/SoC sensors, then ACPI's board zone. A zone of any
// other type (a Wi-Fi card's, say) is only a last resort.
var thermalZonePreference = []string{
	"x86_pkg_temp", "cpu-thermal", "cpu_thermal", "soc_dts0", "soc_dts1", "acpitz",
}

// pickThermalZone returns the zone directory to sample under dir
// (/sys/class/thermal): the best-ranked type that reads, else the first
// zone that reads at all.
func pickThermalZone(dir string) (string, bool) {
	zones, _ := filepath.Glob(filepath.Join(dir, "thermal_zone*"))
	best, bestRank := "", len(thermalZonePreference)+1
	for _, z := range zones {
		if _, err := readZoneTempC(z); err != nil {
			continue
		}
		rank := len(thermalZonePreference)
		if typ, err := os.ReadFile(filepath.Join(z, "type")); err == nil {
			for i, want := range thermalZonePreference {
				if strings.TrimSpace(string(typ)) == want {
					rank = i
					break
				}
			}
		}
		if rank < bestRank {
			best, bestRank = z, rank
		}
	}
	return best, best != ""
}

// readZoneTempC reads a thermal zone's temp (millidegrees) as Celsius.
func readZoneTempC(zone string) (float64, error) {
	data, err := os.ReadFile(filepath.Join(zone, "temp"))
	if err != nil {
		return 0, err
	}
	milli, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	return float64(milli) / 1000, nil
}
