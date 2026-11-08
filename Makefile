# Make recipes must be indented with a tab.
VERSION ?= $(or $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -X main.Version=$(VERSION)
.PHONY: build test vet build-win build-linux build-macos build-macos-arm

build:
	go build -ldflags "$(LDFLAGS)" -o build/gtr-manager ./cmd/gtr-manager
test:
	go test -race ./...
vet:
	go vet ./...
build-win:
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o build/gtr-manager.exe ./cmd/gtr-manager
build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o build/gtr-manager-linux ./cmd/gtr-manager
build-macos:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o build/gtr-manager-darwin ./cmd/gtr-manager
build-macos-arm:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o build/gtr-manager-darwin-arm64 ./cmd/gtr-manager
