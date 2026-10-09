# Tests for the boot config parser/applier installed from
# stage-syncphony/03-config/files/syncphony-box-config. The tests run the
# script straight from the repo so CI covers the parsing without building
# the image; the CI smoke test covers the install inside the image.

BATS_TEST_DIRNAME_ABS="$(cd "${BATS_TEST_DIRNAME}" && pwd)"
SCRIPT="${BATS_TEST_DIRNAME_ABS}/../stage-syncphony/03-config/files/syncphony-box-config"
TEMPLATE="${BATS_TEST_DIRNAME_ABS}/../stage-syncphony/03-config/files/syncphony.txt"

setup() {
	WORK="$(mktemp -d)"
	TXT="${WORK}/syncphony.txt"
	ENV_OUT="${WORK}/config.env"
	CMDLINE="${WORK}/cmdline.txt"
	HOST_OUT="${WORK}/hostname"
	REBOOT_FLAG="${WORK}/rebooted"
	printf 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4\n' > "${CMDLINE}"
}

teardown() {
	rm -rf "${WORK}"
}

txt() {
	printf '%b' "$1" > "${TXT}"
}

apply() {
	SB_TXT="${TXT}" SB_ENV="${ENV_OUT}" SB_CMDLINE="${CMDLINE}" \
	SB_HOSTNAME_CMD="printf %s \"\$1\" > '${HOST_OUT}'" \
	SB_REBOOT_CMD="touch '${REBOOT_FLAG}'" \
		run "${SCRIPT}" apply
}

cmdline_is() {
	[ "$(cat "${CMDLINE}")" = "$1" ]
}

@test "missing file yields every default" {
	rm -f "${TXT}"
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "SERVER_URL=''"
	echo "$output" | grep -Fxq "NAME=''"
	echo "$output" | grep -Fxq "HOSTNAME=''"
	echo "$output" | grep -Fxq "AUDIO='hdmi'"
	echo "$output" | grep -Fxq "RESOLUTION=''"
	echo "$output" | grep -Fxq "ROTATE='0'"
	echo "$output" | grep -Fxq "CEC='on'"
}

@test "the shipped template is fully commented out (all defaults)" {
	run "${SCRIPT}" parse "${TEMPLATE}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "SERVER_URL=''"
	echo "$output" | grep -Fxq "NAME=''"
	echo "$output" | grep -Fxq "AUDIO='hdmi'"
	echo "$output" | grep -Fxq "ROTATE='0'"
	echo "$output" | grep -Fxq "CEC='on'"
}

@test "accepts a UTF-8 BOM and CRLF line endings (Notepad)" {
	txt '\xef\xbb\xbfserver_url=https://s.example\r\nname=My TV\r\naudio=usb\r\n'
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "SERVER_URL='https://s.example'"
	echo "$output" | grep -Fxq "NAME='My TV'"
	echo "$output" | grep -Fxq "HOSTNAME='my-tv'"
	echo "$output" | grep -Fxq "AUDIO='usb'"
}

@test "all six keys parse together" {
	txt 'server_url=https://s.example\nname=Den TV\naudio=analog\nresolution=1920x1080@60\nrotate=180\ncec=off\n'
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "SERVER_URL='https://s.example'"
	echo "$output" | grep -Fxq "NAME='Den TV'"
	echo "$output" | grep -Fxq "HOSTNAME='den-tv'"
	echo "$output" | grep -Fxq "AUDIO='analog'"
	echo "$output" | grep -Fxq "RESOLUTION='1920x1080@60'"
	echo "$output" | grep -Fxq "ROTATE='180'"
	echo "$output" | grep -Fxq "CEC='off'"
}

@test "unknown keys warn on stderr and are ignored" {
	txt 'server_url=https://s.example\nwhatever=1\nno_equals_sign\n'
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -q "ignoring unknown key 'whatever'"
	echo "$output" | grep -q "ignoring unrecognized line: no_equals_sign"
	! echo "$output" | grep -q "^WHATEVER="
}

@test "invalid server_url warns and stays empty" {
	txt 'server_url=ftp://nope.example\n'
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "SERVER_URL=''"
	echo "$output" | grep -q "invalid server_url 'ftp://nope.example'"
}

@test "invalid audio, rotate, cec and resolution fall back to defaults" {
	txt 'audio=spdif\nrotate=45\ncec=yes\nresolution=1080p\n'
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "AUDIO='hdmi'"
	echo "$output" | grep -Fxq "ROTATE='0'"
	echo "$output" | grep -Fxq "CEC='on'"
	echo "$output" | grep -Fxq "RESOLUTION=''"
	echo "$output" | grep -q "invalid audio 'spdif'"
	echo "$output" | grep -q "invalid rotate '45'"
	echo "$output" | grep -q "invalid cec 'yes'"
	echo "$output" | grep -q "invalid resolution '1080p'"
}

@test "resolution accepts WxH and WxH@refresh" {
	txt 'resolution=1280x720\n'
	run "${SCRIPT}" parse "${TXT}"
	echo "$output" | grep -Fxq "RESOLUTION='1280x720'"
	txt 'resolution=1920x1080@50\n'
	run "${SCRIPT}" parse "${TXT}"
	echo "$output" | grep -Fxq "RESOLUTION='1920x1080@50'"
}

@test "slugify: spaces collapse, symbols drop, case folds" {
	txt 'name=Living room TV\n'
	run "${SCRIPT}" parse "${TXT}"
	echo "$output" | grep -Fxq "HOSTNAME='living-room-tv'"
	txt 'name=Caf\xc3\xa9 & Bar!\n'
	run "${SCRIPT}" parse "${TXT}"
	echo "$output" | grep -Fxq "HOSTNAME='caf-bar'"
	txt 'name=  --Den--  \n'
	run "${SCRIPT}" parse "${TXT}"
	echo "$output" | grep -Fxq "HOSTNAME='den'"
}

@test "a name with no letters or digits leaves the hostname alone" {
	txt 'name=???\n'
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "NAME='???'"
	echo "$output" | grep -Fxq "HOSTNAME=''"
	echo "$output" | grep -q "no letters or digits"
}

@test "values with single quotes survive a round trip through sourcing" {
	txt "name=O'Neil's TV\n"
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	eval "$output"
	[ "${NAME}" = "O'Neil's TV" ]
	[ "${HOSTNAME}" = "o-neil-s-tv" ]
}

@test "duplicate keys: the last one wins" {
	txt 'server_url=https://first.example\nserver_url=https://second.example\n'
	run "${SCRIPT}" parse "${TXT}"
	echo "$output" | grep -Fxq "SERVER_URL='https://second.example'"
}

@test "keys are case-insensitive and whitespace is trimmed" {
	txt '  SERVER_URL = https://s.example  \n\taudio\t=\tusb\t\n'
	run "${SCRIPT}" parse "${TXT}"
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "SERVER_URL='https://s.example'"
	echo "$output" | grep -Fxq "AUDIO='usb'"
}

@test "apply writes config.env and leaves the cmdline alone when nothing display-related is set" {
	txt 'server_url=https://s.example\naudio=usb\n'
	apply
	[ "$status" -eq 0 ]
	[ -f "${ENV_OUT}" ]
	. "${ENV_OUT}"
	[ "${SERVER_URL}" = "https://s.example" ]
	[ "${AUDIO}" = "usb" ]
	cmdline_is 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4'
	[ ! -e "${REBOOT_FLAG}" ]
}

@test "apply sets the hostname from name= and does not touch it without name=" {
	txt 'name=Living room TV\n'
	apply
	[ "$(cat "${HOST_OUT}")" = "living-room-tv" ]
	rm -f "${HOST_OUT}"
	txt 'server_url=https://s.example\n'
	apply
	[ ! -e "${HOST_OUT}" ]
}

@test "apply appends video= for a forced resolution and reboots once" {
	txt 'resolution=1920x1080@60\n'
	apply
	cmdline_is 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4 video=HDMI-A-1:1920x1080@60D'
	[ -e "${REBOOT_FLAG}" ]
}

@test "a second apply with the same config is a no-op (no reboot loop)" {
	txt 'resolution=1920x1080@60\n'
	apply
	rm -f "${REBOOT_FLAG}"
	apply
	[ "$status" -eq 0 ]
	[ ! -e "${REBOOT_FLAG}" ]
	cmdline_is 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4 video=HDMI-A-1:1920x1080@60D'
}

@test "clearing resolution removes the video= token" {
	txt 'resolution=1920x1080@60\n'
	apply
	txt 'resolution=\n'
	rm -f "${REBOOT_FLAG}"
	apply
	cmdline_is 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4'
	[ -e "${REBOOT_FLAG}" ]
}

@test "rotate=90 without a resolution still forces the output on" {
	txt 'rotate=90\n'
	apply
	cmdline_is 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4 video=HDMI-A-1:D,rotate=90'
}

@test "resolution and rotate combine into one token" {
	txt 'resolution=1920x1080@60\nrotate=270\n'
	apply
	cmdline_is 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4 video=HDMI-A-1:1920x1080@60D,rotate=270'
}

@test "rotate=0 adds no rotate option" {
	txt 'resolution=1280x720\nrotate=0\n'
	apply
	cmdline_is 'console=serial0,115200 root=PARTUUID=abcd-02 rootfstype=ext4 video=HDMI-A-1:1280x720D'
}

@test "other connectors' video= tokens are preserved" {
	printf 'console=serial0,115200 video=DSI-1:d root=PARTUUID=abcd-02\n' > "${CMDLINE}"
	txt 'resolution=1920x1080\n'
	apply
	cmdline_is 'console=serial0,115200 video=DSI-1:d root=PARTUUID=abcd-02 video=HDMI-A-1:1920x1080D'
}

@test "apply survives an unwritable cmdline: no reboot, still exit 0" {
	mkdir -p "${WORK}/dircmdline"
	SB_TXT="${TXT}" SB_ENV="${ENV_OUT}" SB_CMDLINE="${WORK}/dircmdline" \
	SB_HOSTNAME_CMD=true SB_REBOOT_CMD="touch '${REBOOT_FLAG}'" \
		run "${SCRIPT}" apply
	[ "$status" -eq 0 ]
	[ ! -e "${REBOOT_FLAG}" ]
}

@test "apply with a missing syncphony.txt writes defaults and never fails" {
	rm -f "${TXT}"
	apply
	[ "$status" -eq 0 ]
	[ -f "${ENV_OUT}" ]
	. "${ENV_OUT}"
	[ -z "${SERVER_URL}" ]
	[ "${AUDIO}" = "hdmi" ]
	[ ! -e "${REBOOT_FLAG}" ]
}

@test "parse without a file argument reads SB_TXT" {
	txt 'server_url=https://s.example\n'
	SB_TXT="${TXT}" run "${SCRIPT}" parse
	[ "$status" -eq 0 ]
	echo "$output" | grep -Fxq "SERVER_URL='https://s.example'"
}
