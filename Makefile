.PHONY: image clean test

image:
	./build.sh

test:
	@command -v bats >/dev/null 2>&1 || { echo "bats is required (e.g. apt install bats)"; exit 1; }
	bats tests/

clean:
	rm -rf deploy .build
	-docker rm -v pigen_work >/dev/null 2>&1 || true
