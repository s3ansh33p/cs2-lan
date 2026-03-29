.PHONY: build css run clean

css:
	npx @tailwindcss/cli -i ./web/static/input.css -o ./web/static/tailwind.css --minify

build: css
	CGO_ENABLED=0 go build -o cs2-panel ./cmd/panel

run: build
	./cs2-panel --password admin --compose-file ../docker-compose.yml

clean:
	rm -f cs2-panel
