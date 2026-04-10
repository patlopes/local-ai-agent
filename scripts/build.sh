#!/usr/bin/env bash
# build.sh — Cross-compile the Local AI Agent for all platforms.
#
# Usage:
#   ./scripts/build.sh           # build for current OS
#   ./scripts/build.sh all       # build for all platforms
#   ./scripts/build.sh linux     # build for Linux only

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BIN_DIR="$PROJECT_DIR/bin"
DIST_DIR="$PROJECT_DIR/dist"

VERSION="${VERSION:-1.0.0}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X main.version=$VERSION -X main.buildTime=$BUILD_TIME"

mkdir -p "$BIN_DIR" "$DIST_DIR"

build_binary() {
  local os="$1"
  local arch="$2"
  local output="$BIN_DIR/local-ai-agent-${os}-${arch}"

  if [ "$os" = "windows" ]; then
    output="${output}.exe"
  fi

  echo "Building for ${os}/${arch}..."
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
    -ldflags "$LDFLAGS" \
    -trimpath \
    -o "$output" \
    "$PROJECT_DIR"

  echo "  → $output ($(du -h "$output" | cut -f1))"
}

package_dist() {
  local os="$1"
  local arch="$2"
  local binary="$BIN_DIR/local-ai-agent-${os}-${arch}"
  local dist_name="local-ai-agent-${VERSION}-${os}-${arch}"
  local dist_path="$DIST_DIR/$dist_name"

  if [ "$os" = "windows" ]; then
    binary="${binary}.exe"
  fi

  echo "Packaging ${dist_name}..."
  mkdir -p "$dist_path/ollama"

  cp "$binary" "$dist_path/"
  cp "$PROJECT_DIR/README.md" "$dist_path/" 2>/dev/null || true

  # Include ollama binary if present (download it per-platform first)
  # Users should run download-ollama.sh for each target platform

  if [ "$os" = "windows" ]; then
    (cd "$DIST_DIR" && zip -r "${dist_name}.zip" "$dist_name")
  else
    (cd "$DIST_DIR" && tar czf "${dist_name}.tar.gz" "$dist_name")
  fi

  rm -rf "$dist_path"
  echo "  → $DIST_DIR/${dist_name}.*"
}

TARGET="${1:-current}"

case "$TARGET" in
  current)
    CURRENT_OS="$(go env GOOS)"
    CURRENT_ARCH="$(go env GOARCH)"
    build_binary "$CURRENT_OS" "$CURRENT_ARCH"
    ;;
  linux)
    build_binary linux amd64
    build_binary linux arm64
    ;;
  darwin|macos)
    build_binary darwin amd64
    build_binary darwin arm64
    ;;
  windows)
    build_binary windows amd64
    build_binary windows arm64
    ;;
  all)
    build_binary linux amd64
    build_binary linux arm64
    build_binary darwin amd64
    build_binary darwin arm64
    build_binary windows amd64
    build_binary windows arm64

    echo ""
    echo "Packaging distributions..."
    package_dist linux amd64
    package_dist linux arm64
    package_dist darwin amd64
    package_dist darwin arm64
    package_dist windows amd64
    package_dist windows arm64
    ;;
  *)
    echo "Usage: $0 [current|linux|darwin|windows|all]"
    exit 1
    ;;
esac

echo ""
echo "✓ Build complete!"
ls -la "$BIN_DIR/"
