#!/usr/bin/env bash
# download-ollama.sh — Download Ollama binary for the current or specified platform.
#
# Usage:
#   ./scripts/download-ollama.sh              # auto-detect OS/arch
#   ./scripts/download-ollama.sh linux amd64  # explicit

set -euo pipefail

OLLAMA_VERSION="${OLLAMA_VERSION:-0.6.2}"

TARGET_OS="${1:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
TARGET_ARCH="${2:-$(uname -m)}"

# Normalize arch
case "$TARGET_ARCH" in
  x86_64|amd64) TARGET_ARCH="amd64" ;;
  aarch64|arm64) TARGET_ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $TARGET_ARCH"
    exit 1
    ;;
esac

# Normalize OS
case "$TARGET_OS" in
  linux)   TARGET_OS="linux" ;;
  darwin)  TARGET_OS="darwin" ;;
  windows) TARGET_OS="windows" ;;
  *)
    echo "Unsupported OS: $TARGET_OS"
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OUTPUT_DIR="$PROJECT_DIR/ollama"

mkdir -p "$OUTPUT_DIR"

BINARY_NAME="ollama"
if [ "$TARGET_OS" = "windows" ]; then
  BINARY_NAME="ollama.exe"
fi

OUTPUT_PATH="$OUTPUT_DIR/$BINARY_NAME"

if [ -f "$OUTPUT_PATH" ]; then
  echo "Ollama binary already exists at $OUTPUT_PATH"
  echo "Delete it and re-run to download a fresh copy."
  exit 0
fi

# Construct download URL
# Ollama releases: https://github.com/ollama/ollama/releases
if [ "$TARGET_OS" = "linux" ]; then
  URL="https://github.com/ollama/ollama/releases/download/v${OLLAMA_VERSION}/ollama-linux-${TARGET_ARCH}"
elif [ "$TARGET_OS" = "darwin" ]; then
  # macOS uses a zip with a universal binary
  URL="https://github.com/ollama/ollama/releases/download/v${OLLAMA_VERSION}/ollama-darwin"
elif [ "$TARGET_OS" = "windows" ]; then
  URL="https://github.com/ollama/ollama/releases/download/v${OLLAMA_VERSION}/ollama-windows-${TARGET_ARCH}.exe"
fi

echo "Downloading Ollama v${OLLAMA_VERSION} for ${TARGET_OS}/${TARGET_ARCH}..."
echo "URL: $URL"
echo "Output: $OUTPUT_PATH"

curl -fSL --progress-bar "$URL" -o "$OUTPUT_PATH"
chmod +x "$OUTPUT_PATH"

echo ""
echo "✓ Ollama downloaded successfully to $OUTPUT_PATH"
echo "  Version: ${OLLAMA_VERSION}"
echo "  OS/Arch: ${TARGET_OS}/${TARGET_ARCH}"
