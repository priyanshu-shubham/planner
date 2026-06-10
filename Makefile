.PHONY: build frontend go cli-dist test clean install

# Platforms whose CLI binaries the server distributes at /cli/{platform}.
CLI_PLATFORMS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64

# Full build: frontend bundle (npm/esbuild) then the Go binary with assets embedded.
build: frontend go

frontend:
	cd web/frontend && npm install --no-audit --no-fund && npm run build

go:
	go build -o planner .

# Cross-compile the CLI for every distributed platform into cli-dist/, stored
# gzipped (about 65% smaller). The server serves these at /cli/{platform} when
# $PLANNER_CLI_DIR points at the directory (the Docker image does this; see
# Dockerfile), decompressing on the fly for clients that don't accept gzip.
cli-dist:
	@mkdir -p cli-dist
	@for p in $(CLI_PLATFORMS); do \
		os=$${p%-*}; arch=$${p#*-}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		echo "building cli-dist/planner-$$p$$ext.gz"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" \
			-o cli-dist/planner-$$p$$ext . || exit 1; \
		gzip -9 -f cli-dist/planner-$$p$$ext || exit 1; \
	done

# Build then install the binary to ~/.local/bin.
install: build
	mkdir -p $(HOME)/.local/bin
	install -m 0755 planner $(HOME)/.local/bin/planner

test:
	go test ./...

clean:
	rm -f planner
	rm -rf cli-dist
	rm -f internal/web/static/bundle.js internal/web/static/bundle.css
