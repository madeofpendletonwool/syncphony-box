.PHONY: image clean test lint

image:
	./build.sh

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

clean:
	rm -rf deploy .build
	rm -f stage-syncphony/05-boxd/files/boxd
	-docker rm -v pigen_work >/dev/null 2>&1 || true
