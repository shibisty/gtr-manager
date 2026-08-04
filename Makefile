build:
    go build -o build/gtr-manager ./cmd/gtr-manager
build-win:
    GOOS=windows GOARCH=amd64 go build -o build/gtr-manager.exe ./cmd/gtr-manager
build-linux:
    GOOS=linux GOARCH=amd64 go build -o build/gtr-manager-linux ./cmd/gtr-manager
build-macos:
    GOOS=darwin GOARCH=amd64 go build -o build/gtr-manager-darwin ./cmd/gtr-manager
build-macos-arm:
    GOOS=darwin GOARCH=arm64 go build -o build/gtr-manager-darwin-arm64 ./cmd/gtr-manager
