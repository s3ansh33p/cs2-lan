.PHONY: build run clean

build:
	CGO_ENABLED=0 go build -o cs2-panel ./cmd/panel

run: build
	./cs2-panel --password admin --compose-file ../docker-compose.yml

clean:
	rm -f cs2-panel
