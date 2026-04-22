app     := "chrome-profile-cloner"
cmd     := "cmd/" + app
outdir  := "dist"

# Default: show available recipes
default:
    @just --list

# ── Development ──────────────────────────────────────────────────────────────

# Build for the current OS/arch
build:
    go build -o {{outdir}}/{{app}} ./{{cmd}}
    @echo "Built → {{outdir}}/{{app}}"

# Build with version/commit info stamped in (requires git)
build-release:
    #!/usr/bin/env sh
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    LDFLAGS="-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildTime=${BUILD_TIME}"
    mkdir -p {{outdir}}
    go build -ldflags "${LDFLAGS}" -o {{outdir}}/{{app}} ./{{cmd}}
    echo "Built → {{outdir}}/{{app}} (${VERSION} @ ${COMMIT})"

# Run the app (pass args after --: just run -- --dry-run)
run *args:
    go run ./{{cmd}} {{args}}

# Run tests
test:
    go test ./...

# Run tests with verbose output
test-verbose:
    go test -v ./...

# Run tests with race detector
test-race:
    go test -race ./...

# Format all Go source files
fmt:
    gofmt -w .

# Run go vet
vet:
    go vet ./...

# Tidy go.mod / go.sum
tidy:
    go mod tidy

# ── Cross-compilation ─────────────────────────────────────────────────────────

# Build for all supported platforms
build-all: build-linux build-macos build-windows
    @echo "All platform builds complete → {{outdir}}/"

# Linux (amd64 + arm64)
build-linux:
    @mkdir -p {{outdir}}
    GOOS=linux GOARCH=amd64  go build -o {{outdir}}/{{app}}-linux-amd64   ./{{cmd}}
    GOOS=linux GOARCH=arm64  go build -o {{outdir}}/{{app}}-linux-arm64   ./{{cmd}}
    @echo "Built → {{outdir}}/{{app}}-linux-amd64"
    @echo "Built → {{outdir}}/{{app}}-linux-arm64"

# macOS (amd64 + arm64 / Apple Silicon)
build-macos:
    @mkdir -p {{outdir}}
    GOOS=darwin GOARCH=amd64 go build -o {{outdir}}/{{app}}-macos-amd64   ./{{cmd}}
    GOOS=darwin GOARCH=arm64 go build -o {{outdir}}/{{app}}-macos-arm64   ./{{cmd}}
    @echo "Built → {{outdir}}/{{app}}-macos-amd64"
    @echo "Built → {{outdir}}/{{app}}-macos-arm64"

# Windows (amd64 + arm64)
build-windows:
    @mkdir -p {{outdir}}
    GOOS=windows GOARCH=amd64 go build -o {{outdir}}/{{app}}-windows-amd64.exe ./{{cmd}}
    GOOS=windows GOARCH=arm64 go build -o {{outdir}}/{{app}}-windows-arm64.exe ./{{cmd}}
    @echo "Built → {{outdir}}/{{app}}-windows-amd64.exe"
    @echo "Built → {{outdir}}/{{app}}-windows-arm64.exe"

# Build a universal macOS binary (requires both arch builds first)
build-macos-universal: build-macos
    lipo -create -output {{outdir}}/{{app}}-macos-universal \
        {{outdir}}/{{app}}-macos-amd64 \
        {{outdir}}/{{app}}-macos-arm64
    @echo "Built → {{outdir}}/{{app}}-macos-universal"

# ── Release ───────────────────────────────────────────────────────────────────

# Build all platforms with release flags (stripped, no debug info)
release:
    #!/usr/bin/env sh
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    LDFLAGS="-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildTime=${BUILD_TIME}"
    mkdir -p {{outdir}}
    targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
    for target in $targets; do
        os=$(echo $target | cut -d/ -f1)
        arch=$(echo $target | cut -d/ -f2)
        ext=""
        [ "$os" = "windows" ] && ext=".exe"
        out="{{outdir}}/{{app}}-${os}-${arch}${ext}"
        echo "Building $out ..."
        GOOS=$os GOARCH=$arch go build -ldflags "${LDFLAGS}" -o "$out" ./{{cmd}}
    done
    echo "Release builds complete (${VERSION})"

# ── Cleanup ───────────────────────────────────────────────────────────────────

# Remove compiled binaries
clean:
    rm -rf {{outdir}}
    @echo "Cleaned {{outdir}}/"

# Install the binary to $GOPATH/bin (or ~/go/bin)
install:
    go install ./{{cmd}}
    @echo "Installed {{app}} to $(go env GOPATH)/bin"
