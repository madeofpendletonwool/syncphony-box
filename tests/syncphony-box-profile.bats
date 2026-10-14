# Tests for the Chromium profile handling in boxd (MAD-804, ADR 0006):
# prep clears the unclean-exit state Chromium records after a power cut,
# reset wipes the profile so the box re-pairs, and path is the single
# source for where the profile lives. Like the config bats suite, these
# run a host-arch boxd built from the repo (make test builds it into
# .build/boxd; BOXD_BIN overrides).

BATS_TEST_DIRNAME_ABS="$(cd "${BATS_TEST_DIRNAME}" && pwd)"
SCRIPT="${BOXD_BIN:-${BATS_TEST_DIRNAME_ABS}/../.build/boxd}"

setup() {
	WORK="$(mktemp -d)"
	PROFILE="${WORK}/chromium"
	mkdir -p "${PROFILE}/Default"
	printf '{"browser":{"exit_type":"Crashed","restore_on_startup":4},"session":{"done":true}}' \
		> "${PROFILE}/Default/Preferences"
	printf '{"browser":{"enabled_labs_experiments":["fl@1"]}}' \
		> "${PROFILE}/Local State"
}

teardown() {
	rm -rf "${WORK}"
}

@test "path prints the default profile dir" {
	run "${SCRIPT}" profile path
	[ "$status" -eq 0 ]
	[ "$output" = "/data/chromium" ]
}

@test "path honors SB_PROFILE" {
	SB_PROFILE="${WORK}/dev-profile" run "${SCRIPT}" profile path
	[ "$status" -eq 0 ]
	[ "$output" = "${WORK}/dev-profile" ]
}

@test "prep clears exit_type and exited_cleanly, keeping everything else" {
	run "${SCRIPT}" profile prep "${PROFILE}"
	[ "$status" -eq 0 ]

	prefs="$(cat "${PROFILE}/Default/Preferences")"
	echo "$prefs" | grep -q '"exit_type":"Normal"'
	echo "$prefs" | grep -q '"restore_on_startup":4'
	echo "$prefs" | grep -q '"session":{"done":true}'

	local_state="$(cat "${PROFILE}/Local State")"
	echo "$local_state" | grep -q '"exited_cleanly":true'
	echo "$local_state" | grep -q '"enabled_labs_experiments"'
}

@test "prep leaves a fresh (empty) profile alone and succeeds" {
	empty="${WORK}/fresh"
	mkdir -p "${empty}"
	run "${SCRIPT}" profile prep "${empty}"
	[ "$status" -eq 0 ]
	[ -z "$(ls -A "${empty}")" ]
}

@test "prep fails cleanly on corrupt JSON and leaves it untouched" {
	printf 'not json' > "${PROFILE}/Default/Preferences"
	run "${SCRIPT}" profile prep "${PROFILE}"
	[ "$status" -eq 1 ]
	[ "$(cat "${PROFILE}/Default/Preferences")" = "not json" ]
}

@test "reset wipes the profile" {
	run "${SCRIPT}" profile reset "${PROFILE}"
	[ "$status" -eq 0 ]
	[ ! -e "${PROFILE}" ]
}

@test "reset without SB_PROFILE refuses dangerous paths" {
	run "${SCRIPT}" profile reset ""
	[ "$status" -eq 1 ]
	run "${SCRIPT}" profile reset "relative/chromium"
	[ "$status" -eq 1 ]
}

@test "reset honors SB_PROFILE for the default target" {
	cp -r "${PROFILE}" "${WORK}/dev-profile"
	SB_PROFILE="${WORK}/dev-profile" run "${SCRIPT}" profile reset
	[ "$status" -eq 0 ]
	[ ! -e "${WORK}/dev-profile" ]
	[ -e "${PROFILE}" ]
}
