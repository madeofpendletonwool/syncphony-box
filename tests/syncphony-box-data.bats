# Tests for data-persist — the script that moves the account files, SSH
# host keys and NetworkManager connections across the /data boundary on a
# read-only-root box (MAD-805, ADR 0007). It runs straight from the stage
# tree; SB_DATA_DIR and SB_ETC_DIR point it at temp dirs (and imply dev
# mode: no mount check, no ssh-keygen).

BATS_TEST_DIRNAME_ABS="$(cd "${BATS_TEST_DIRNAME}" && pwd)"
SCRIPT="${BATS_TEST_DIRNAME_ABS}/../stage-syncphony/06-readonly-root/files/data-persist"

setup() {
	WORK="$(mktemp -d)"
	DATA="${WORK}/data"
	ETC="${WORK}/etc"
	mkdir -p "${DATA}" "${ETC}/ssh"
	export SB_DATA_DIR="${DATA}" SB_ETC_DIR="${ETC}"
}

teardown() {
	rm -rf "${WORK}"
}

fake_identity() {
	printf 'root:x:0:0:root:/root:/bin/bash\nsyncphony:x:1000:1000::/home/syncphony:/bin/bash\n' > "${ETC}/passwd"
	printf 'root:*:20000:0:99999:7:::\nsyncphony:$6$rounds:20000:0:99999:7:::\n' > "${ETC}/shadow"
	printf 'root:x:0:\n' > "${ETC}/group"
	printf 'root:*::\n' > "${ETC}/gshadow"
	printf 'syncphony:100000:65536\n' > "${ETC}/subuid"
	printf 'syncphony:100000:65536\n' > "${ETC}/subgid"
}

@test "restore on a fresh /data creates the persistent layout and is a no-op" {
	run "${SCRIPT}" restore
	[ "$status" -eq 0 ]
	for d in chromium journal syncphony-box boxd system/cloud system/identity system/ssh system/network; do
		[ -d "${DATA}/${d}" ]
	done
}

@test "save then restore round-trips the account files" {
	fake_identity
	run "${SCRIPT}" save
	[ "$status" -eq 0 ]
	[ -f "${DATA}/system/identity/passwd" ]
	cmp "${ETC}/passwd" "${DATA}/system/identity/passwd"
	cmp "${ETC}/shadow" "${DATA}/system/identity/shadow"

	# Lose the overlay (fresh boot): /etc back to the image's files.
	printf 'root:x:0:0:root:/root:/usr/sbin/nologin\n' > "${ETC}/passwd"
	printf 'root:*:20000:0:99999:7:::\n' > "${ETC}/shadow"
	run "${SCRIPT}" restore
	[ "$status" -eq 0 ]
	grep -q '^syncphony:' "${ETC}/passwd"
	grep -q '^syncphony:' "${ETC}/shadow"
}

@test "save then restore round-trips the SSH host keys" {
	mkdir -p "${DATA}/system/ssh"
	printf 'PRIVATE-KEY-DATA' > "${ETC}/ssh/ssh_host_ed25519_key"
	printf 'ssh-ed25519 AAAA box' > "${ETC}/ssh/ssh_host_ed25519_key.pub"
	run "${SCRIPT}" save
	[ "$status" -eq 0 ]
	cmp "${ETC}/ssh/ssh_host_ed25519_key" "${DATA}/system/ssh/ssh_host_ed25519_key"

	rm "${ETC}/ssh/ssh_host_ed25519_key"
	run "${SCRIPT}" restore
	[ "$status" -eq 0 ]
	grep -q 'PRIVATE-KEY-DATA' "${ETC}/ssh/ssh_host_ed25519_key"
}

@test "save persists the machine-id for the initramfs seed" {
	printf '0123456789abcdef0123456789abcdef\n' > "${ETC}/machine-id"
	run "${SCRIPT}" save
	[ "$status" -eq 0 ]
	cmp "${ETC}/machine-id" "${DATA}/system/machine-id"
}

@test "NetworkManager connections round-trip through /data" {
	fake_identity
	mkdir -p "${ETC}/NetworkManager/system-connections"
	printf '[connection]\nid=home\n' > "${ETC}/NetworkManager/system-connections/home.nmconnection"
	run "${SCRIPT}" save
	[ "$status" -eq 0 ]
	[ -f "${DATA}/system/network/home.nmconnection" ]

	rm -rf "${ETC}/NetworkManager/system-connections"
	mkdir -p "${ETC}/NetworkManager/system-connections"
	run "${SCRIPT}" restore
	[ "$status" -eq 0 ]
	grep -q 'id=home' "${ETC}/NetworkManager/system-connections/home.nmconnection"
}

@test "no NetworkManager dir is fine" {
	run "${SCRIPT}" save
	[ "$status" -eq 0 ]
	run "${SCRIPT}" restore
	[ "$status" -eq 0 ]
}

@test "an unmounted /data (real path) is a quiet no-op" {
	unset SB_DATA_DIR
	# /data does not exist on the test host; even if it did, it would not
	# be a mount point here. Both verbs must exit 0 without touching it.
	run "${SCRIPT}" restore
	[ "$status" -eq 0 ]
	run "${SCRIPT}" save
	[ "$status" -eq 0 ]
}

@test "usage errors out" {
	run "${SCRIPT}"
	[ "$status" -eq 2 ]
}
