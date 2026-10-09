.PHONY: image clean

image:
	./build.sh

clean:
	rm -rf deploy .build
	-docker rm -v pigen_work >/dev/null 2>&1 || true
