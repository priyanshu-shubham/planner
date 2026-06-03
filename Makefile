.PHONY: build frontend go test clean install

# Full build: frontend bundle (npm/esbuild) then the Go binary with assets embedded.
build: frontend go

frontend:
	cd web/frontend && npm install --no-audit --no-fund && npm run build

go:
	go build -o planner .

# Build then install the binary to ~/.local/bin.
install: build
	mkdir -p $(HOME)/.local/bin
	install -m 0755 planner $(HOME)/.local/bin/planner

test:
	go test ./...

clean:
	rm -f planner
	rm -f internal/web/static/bundle.js internal/web/static/bundle.css
