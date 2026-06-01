# Docker Commander build targets.
#
# `make build` produces a single self-contained binary with the web UI embedded.
# `make release` cross-compiles for Linux, macOS (Intel + Apple Silicon) and
# Windows. All builds are CGO-free thanks to the pure-Go SQLite driver.

BINARY := dockercmd
PKG    := ./cmd/dockercmd
OUT    := dist-bin
VERSION ?= dev
LDFLAGS := -s -w

.PHONY: all build ui run dev test vet clean release

all: build

## ui: build the frontend into web/dist (embedded by the Go build)
ui:
	cd web && npm install && npm run build

## build: build the UI then the binary for the current platform
build: ui
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

## run: build and run locally (serves embedded UI on :8080)
run: build
	./$(BINARY)

## dev: run the API in dev mode (UI served by `cd web && npm run dev`)
dev:
	go run $(PKG) -dev

## test: run Go tests
test:
	go test ./...

## vet: static checks
vet:
	go vet ./...

## clean: remove build artifacts
clean:
	rm -rf $(BINARY) $(OUT) web/dist/assets

## release: cross-compile static binaries for all supported platforms
release: ui
	@mkdir -p $(OUT)
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(OUT)/$(BINARY)-linux-amd64       $(PKG)
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(OUT)/$(BINARY)-linux-arm64       $(PKG)
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(OUT)/$(BINARY)-darwin-amd64      $(PKG)
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(OUT)/$(BINARY)-darwin-arm64      $(PKG)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(OUT)/$(BINARY)-windows-amd64.exe $(PKG)
	@echo "binaries written to $(OUT)/"
