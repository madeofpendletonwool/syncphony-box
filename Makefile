.PHONY: image iso clean test lint reset

image:
	./build.sh

# The x86-64 live/installer ISO (MAD-846): live-build in a Debian container.
iso:
	./build-iso.sh

test: .build/boxd
	@command -v bats >/dev/null 2>&1 || { echo "bats is required (e.g. apt install bats)"; exit 1; }
	@command -v go >/dev/null 2>&1 || { echo "go is required (https://go.dev/dl/)"; exit 1; }
	bats tests/
	cd boxd && go test ./...

lint:
	@command -v go >/dev/null 2>&1 || { echo "go is required (https://go.dev/dl/)"; exit 1; }
	cd boxd && test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	cd boxd && go vet ./...

# Host-arch boxd for the bats tests (they pin the syncphony.txt behavior).
.build/boxd: boxd/cmd/boxd boxd/internal
	@command -v go >/dev/null 2>&1 || { echo "go is required (https://go.dev/dl/)"; exit 1; }
	mkdir -p .build
	cd boxd && go build -mod=vendor -o ../$@ ./cmd/boxd

# Wipe a Chromium profile so the box (or a dev boxd setup) re-pairs from
# scratch. Defaults to the box's profile path; SB_PROFILE=... points it
# somewhere else. On a real box use: sudo syncphony-box reset
reset: .build/boxd
	.build/boxd profile reset

clean:
	rm -rf deploy .build
	rm -rf stage-syncphony/*/files
	-docker rm -v pigen_work >/dev/null 2>&1 || true
