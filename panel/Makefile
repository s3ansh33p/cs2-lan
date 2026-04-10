.PHONY: build css run clean

css:
	npx @tailwindcss/cli -i ./web/static/input.css -o ./web/static/tailwind.css --minify

build: css
	CGO_ENABLED=0 go build -o unilan ./cmd/panel

run: build
	./unilan --password a --compose-file ../docker-compose.yml --tls

clean:
	rm -f unilan
