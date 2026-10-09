# Tests for the audio sink selection installed from
# stage-syncphony/04-audio/files/audio-setup. Like the config tests, they
# run the script straight from the repo, against a fake pactl fed with
# representative `pactl list sinks` listings: the vc4 driver names its HDMI
# PCM devices "bcm2835 HDMI 1" (HDMI0) and "bcm2835 HDMI 2" (HDMI1) on both
# the Pi 4 and the Pi 5, and the jack card is "bcm2835 Headphones". The
# on-Pi checks (exact strings, hotplug, an hour of playback) live in the
# README smoke checklist; CI has no hardware.

BATS_TEST_DIRNAME_ABS="$(cd "${BATS_TEST_DIRNAME}" && pwd)"
SCRIPT="${BATS_TEST_DIRNAME_ABS}/../stage-syncphony/04-audio/files/audio-setup"

setup() {
	WORK="$(mktemp -d)"
	CALLS="${WORK}/calls"
	FIXTURE="${WORK}/sinks.txt"
	FAKE_PACTL="${WORK}/pactl"
	ENV_FILE="${WORK}/config.env"
	: > "${CALLS}"

	cat > "${FAKE_PACTL}" <<EOF
#!/bin/sh
echo "\$*" >> "${CALLS}"
case "\$1" in
	info)
		if [ -s "${WORK}/fail_info" ] && [ "\$(cat "${WORK}/fail_info")" -gt 0 ]; then
			n=\$((\$(cat "${WORK}/fail_info") - 1))
			printf '%s' "\$n" > "${WORK}/fail_info"
			exit 1
		fi
		echo "Server String: unix/\${XDG_RUNTIME_DIR}/pulse/native"
		exit 0
		;;
	list)
		cat "${FIXTURE}"
		;;
	get-default-sink)
		if [ -f "${WORK}/default_sink" ]; then cat "${WORK}/default_sink"; else echo "alsa_output.auto_null"; fi
		;;
	subscribe)
		echo "event 'change' on sink #42"
		sleep 30
		;;
esac
exit 0
EOF
	chmod +x "${FAKE_PACTL}"
}

teardown() {
	rm -rf "${WORK}"
}

apply() {
	SB_PACTL="${FAKE_PACTL}" SB_CONFIG_ENV="${ENV_FILE}" SB_SLEEP=true \
		run "${SCRIPT}" apply
}

set_audio() {
	printf "AUDIO='%s'\n" "$1" > "${ENV_FILE}"
}

# --- fixtures ----------------------------------------------------------------

sinks_pi4() {
	cat > "${FIXTURE}" <<'EOF'
Sink #41
	State: SUSPENDED
	Name: alsa_output.platform-fef00700.hdmi.stereo-fallback
	Description: Built-in Audio Digital Stereo (HDMI)
	Driver: PipeWire
	Mute: no
	Volume: front-left: 49152 /  75% / -8.50 dB,   front-right: 49152 /  75% / -8.50 dB
	        balance 0.00
	Base Volume: 65536 / 100% / 0.00 dB
	Monitor Source: alsa_output.platform-fef00700.hdmi.stereo-fallback.monitor
	Latency: 0 usec, configured 0 usec
	Flags: DECIBEL_VOLUME LATENCY
	Properties:
		alsa.card = "1"
		alsa.card_name = "vc4-hdmi"
		alsa.long_card_name = "vc4-hdmi"
		alsa.name = "bcm2835 HDMI 1"
		alsa.id = "bcm2835 HDMI 1"
		device.description = "Built-in Audio Digital Stereo (HDMI)"
		device.bus_path = "platform-fef00700"
		device.alias = "hdmi0"
		api.alsa.path = "front:hdmi0"
	Formats:
		pcm:

Sink #42
	State: SUSPENDED
	Name: alsa_output.platform-fef00710.hdmi.stereo-fallback
	Description: Built-in Audio Digital Stereo (HDMI 2)
	Driver: PipeWire
	Mute: no
	Volume: front-left: 49152 /  75% / -8.50 dB,   front-right: 49152 /  75% / -8.50 dB
	        balance 0.00
	Base Volume: 65536 / 100% / 0.00 dB
	Monitor Source: alsa_output.platform-fef00710.hdmi.stereo-fallback.monitor
	Latency: 0 usec, configured 0 usec
	Flags: DECIBEL_VOLUME LATENCY
	Properties:
		alsa.card = "2"
		alsa.card_name = "vc4-hdmi"
		alsa.long_card_name = "vc4-hdmi"
		alsa.name = "bcm2835 HDMI 2"
		alsa.id = "bcm2835 HDMI 2"
		device.description = "Built-in Audio Digital Stereo (HDMI 2)"
		device.bus_path = "platform-fef00710"
		device.alias = "hdmi1"
		api.alsa.path = "front:hdmi1"
	Formats:
		pcm:

Sink #45
	State: SUSPENDED
	Name: alsa_output.platform-b1.stereo-fallback
	Description: Built-in Audio Stereo
	Driver: PipeWire
	Mute: no
	Volume: front-left: 49152 /  75% / -8.50 dB,   front-right: 49152 /  75% / -8.50 dB
	        balance 0.00
	Base Volume: 65536 / 100% / 0.00 dB
	Monitor Source: alsa_output.platform-b1.stereo-fallback.monitor
	Latency: 0 usec, configured 0 usec
	Flags: DECIBEL_VOLUME LATENCY
	Properties:
		alsa.card = "0"
		alsa.card_name = "bcm2835 Headphones"
		alsa.long_card_name = "bcm2835 Headphones"
		alsa.name = "bcm2835 Headphones"
		alsa.id = "bcm2835 Headphones"
		device.description = "Built-in Audio Stereo"
		device.bus_path = "platform-b1"
		device.alias = "b1"
		api.alsa.path = "front:b1"
	Formats:
		pcm:
EOF
}

# A Pi 5: two HDMI outputs, no analog jack.
sinks_pi5() {
	sinks_pi4
	sed -i '/^Sink #45/,$d' "${FIXTURE}"
}

add_usb_dac() {
	cat >> "${FIXTURE}" <<'EOF'

Sink #58
	State: SUSPENDED
	Name: alsa_output.usb-Burr-Brown_from_TI_USB_AUDIO_DAC-00.analog-stereo
	Description: USB AUDIO DAC Analog Stereo
	Driver: PipeWire
	Mute: no
	Volume: front-left: 39321 /  60% / -13.00 dB,   front-right: 39321 /  60% / -13.00 dB
	        balance 0.00
	Base Volume: 65536 / 100% / 0.00 dB
	Monitor Source: alsa_output.usb-Burr-Brown_from_TI_USB_AUDIO_DAC-00.analog-stereo.monitor
	Latency: 0 usec, configured 0 usec
	Flags: DECIBEL_VOLUME LATENCY
	Properties:
		alsa.card = "3"
		alsa.card_name = "USB AUDIO DAC"
		alsa.long_card_name = "Burr-Brown from TI USB AUDIO DAC at usb-0000:01:00.0-1.2, full speed"
		alsa.name = "USB Audio"
		device.description = "USB AUDIO DAC Analog Stereo"
		device.bus = "usb"
		device.string = "front:3"
	Formats:
		pcm:
EOF
}

# A USB headset: says "headphones" but is NOT the analog jack.
add_usb_headset() {
	cat >> "${FIXTURE}" <<'EOF'

Sink #61
	State: SUSPENDED
	Name: alsa_output.usb-Plantronics_Plantronics_Headset-00.analog-stereo
	Description: Plantronics Headphones Analog Stereo
	Driver: PipeWire
	Mute: no
	Volume: front-left: 39321 /  60% / -13.00 dB,   front-right: 39321 /  60% / -13.00 dB
	        balance 0.00
	Base Volume: 65536 / 100% / 0.00 dB
	Monitor Source: alsa_output.usb-Plantronics_Plantronics_Headset-00.analog-stereo.monitor
	Latency: 0 usec, configured 0 usec
	Flags: DECIBEL_VOLUME LATENCY
	Properties:
		alsa.card = "4"
		alsa.card_name = "Plantronics Headset"
		alsa.long_card_name = "Plantronics Headset at usb-0000:01:00.0-1.3, full speed"
		alsa.name = "USB Audio"
		device.description = "Plantronics Headphones Analog Stereo"
		device.bus = "usb"
		device.string = "front:4"
	Formats:
		pcm:
EOF
}

# Drop the HDMI0 sink: a TV that is off at boot (or unplugged).
drop_hdmi0() {
	awk '
		/^Sink #41/ { skip = 1 }
		skip && /^Sink #/ && !/^Sink #41/ { skip = 0 }
		!skip { print }
	' "${FIXTURE}" > "${FIXTURE}.new" && mv "${FIXTURE}.new" "${FIXTURE}"
}

mute_sink() {
	awk -v target="$1" '
		/^Sink #/ { blk = 0 }
		/^\tName: / {
			line = $0
			sub(/^\tName: */, "", line)
			if (line == target) blk = 1
		}
		blk && /^\tMute: no/ { print "\tMute: yes"; next }
		{ print }
	' "${FIXTURE}" > "${FIXTURE}.new" && mv "${FIXTURE}.new" "${FIXTURE}"
}

volume_100_sink() {
	awk -v target="$1" '
		/^Sink #/ { blk = 0 }
		/^\tName: / {
			line = $0
			sub(/^\tName: */, "", line)
			if (line == target) blk = 1
		}
		blk && /^\tVolume: / {
			print "\tVolume: front-left: 65536 / 100% / 0.00 dB,   front-right: 65536 / 100% / 0.00 dB"
			next
		}
		{ print }
	' "${FIXTURE}" > "${FIXTURE}.new" && mv "${FIXTURE}.new" "${FIXTURE}"
}

HDMI0=alsa_output.platform-fef00700.hdmi.stereo-fallback
HDMI1=alsa_output.platform-fef00710.hdmi.stereo-fallback
JACK=alsa_output.platform-b1.stereo-fallback
DAC=alsa_output.usb-Burr-Brown_from_TI_USB_AUDIO_DAC-00.analog-stereo

called() { grep -Fqx "$*" "${CALLS}"; }

# --- tests -------------------------------------------------------------------

@test "hdmi picks HDMI0 (the port next to USB-C power) on a Pi 4" {
	sinks_pi4
	set_audio hdmi
	apply
	[ "$status" -eq 0 ]
	called "set-default-sink ${HDMI0}"
	! called "set-default-sink ${HDMI1}"
}

@test "hdmi2 picks HDMI1" {
	sinks_pi4
	set_audio hdmi2
	apply
	[ "$status" -eq 0 ]
	called "set-default-sink ${HDMI1}"
	! called "set-default-sink ${HDMI0}"
}

@test "analog picks the Pi 4's 3.5 mm jack" {
	sinks_pi4
	set_audio analog
	apply
	[ "$status" -eq 0 ]
	called "set-default-sink ${JACK}"
}

@test "usb picks the USB DAC" {
	sinks_pi4
	add_usb_dac
	set_audio usb
	apply
	[ "$status" -eq 0 ]
	called "set-default-sink ${DAC}"
}

@test "hdmi still picks HDMI0 on a Pi 5" {
	sinks_pi5
	set_audio hdmi
	apply
	[ "$status" -eq 0 ]
	called "set-default-sink ${HDMI0}"
}

@test "analog on a Pi 5 (no jack) warns and leaves the default alone" {
	sinks_pi5
	set_audio analog
	apply
	[ "$status" -eq 0 ]
	echo "$output" | grep -q "no sink matches audio=analog"
	! grep -q '^set-default-sink' "${CALLS}"
}

@test "hdmi with the TV off (sink absent) waits, warns, never fails" {
	sinks_pi4
	drop_hdmi0
	set_audio hdmi
	apply
	[ "$status" -eq 0 ]
	echo "$output" | grep -q "no sink matches audio=hdmi"
	# The wrong port is never chosen as a fallback.
	! grep -q '^set-default-sink' "${CALLS}"
}

@test "usb with no USB device warns and leaves the default alone" {
	sinks_pi4
	set_audio usb
	apply
	[ "$status" -eq 0 ]
	echo "$output" | grep -q "no sink matches audio=usb"
	! grep -q '^set-default-sink' "${CALLS}"
}

@test "analog ignores a USB headset that also says headphones" {
	sinks_pi4
	add_usb_headset
	set_audio analog
	apply
	[ "$status" -eq 0 ]
	called "set-default-sink ${JACK}"
}

@test "sets the chosen sink to 100%" {
	sinks_pi4
	set_audio hdmi
	apply
	called "set-sink-volume ${HDMI0} 100%"
}

@test "a muted sink is unmuted" {
	sinks_pi4
	mute_sink "${HDMI0}"
	set_audio hdmi
	apply
	called "set-sink-mute ${HDMI0} 0"
}

@test "already default, 100% and unmuted: no calls (monitor cannot ping-pong)" {
	sinks_pi4
	volume_100_sink "${HDMI0}"
	printf '%s\n' "${HDMI0}" > "${WORK}/default_sink"
	set_audio hdmi
	apply
	[ "$status" -eq 0 ]
	! grep -q '^set-default-sink' "${CALLS}"
	! grep -q '^set-sink-volume' "${CALLS}"
	! grep -q '^set-sink-mute' "${CALLS}"
}

@test "missing config.env defaults to hdmi" {
	sinks_pi4
	rm -f "${ENV_FILE}"
	apply
	[ "$status" -eq 0 ]
	called "set-default-sink ${HDMI0}"
}

@test "invalid AUDIO falls back to hdmi" {
	sinks_pi4
	set_audio spdif
	apply
	[ "$status" -eq 0 ]
	echo "$output" | grep -q "unknown AUDIO 'spdif'"
	called "set-default-sink ${HDMI0}"
}

@test "waits for the PipeWire server to come up before selecting" {
	sinks_pi4
	set_audio hdmi
	printf '2' > "${WORK}/fail_info"
	apply
	[ "$status" -eq 0 ]
	# Two failed probes, then the successful one.
	[ "$(grep -c '^info' "${CALLS}")" -eq 3 ]
	called "set-default-sink ${HDMI0}"
}

@test "a server that never comes up is a warning, not a failure" {
	sinks_pi4
	set_audio hdmi
	printf '999' > "${WORK}/fail_info"
	apply
	[ "$status" -eq 0 ]
	echo "$output" | grep -q "PipeWire server not up"
	! grep -q '^set-default-sink' "${CALLS}"
}

@test "monitor re-applies the selection when a sink appears (hotplug)" {
	sinks_pi4
	set_audio hdmi
	SB_PACTL="${FAKE_PACTL}" SB_CONFIG_ENV="${ENV_FILE}" SB_SLEEP=true \
		run timeout 3 "${SCRIPT}" monitor
	# Killed by timeout while following events.
	[ "$status" -eq 124 ]
	# The initial apply plus the event-driven re-apply.
	[ "$(grep -c '^set-default-sink' "${CALLS}")" -ge 2 ]
}
