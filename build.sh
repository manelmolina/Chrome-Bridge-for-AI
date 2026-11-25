#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_BIN="${GO_BIN:-$(command -v go || echo /tmp/go-toolchain/go/bin/go)}"
DIST_DIR="$ROOT_DIR/dist"
APPIMAGETOOL="${APPIMAGETOOL:-/tmp/appimage-tools/squashfs-root/AppRun}"

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

echo "Running tests..."
(cd "$ROOT_DIR" && "$GO_BIN" vet ./... && "$GO_BIN" test -count=1 -v ./...)

echo "Building Linux AppImage (x86_64)..."
APPDIR="$(mktemp -d)"
trap 'rm -rf "$APPDIR"' EXIT
mkdir -p "$APPDIR/usr/bin"
(cd "$ROOT_DIR" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags="-s -w" -o "$APPDIR/usr/bin/chrome-bridge-for-ai" .)
cp "$ROOT_DIR/packaging/chrome-bridge-for-ai.desktop" "$APPDIR/chrome-bridge-for-ai.desktop"
cp "$ROOT_DIR/packaging/chrome-bridge-for-ai.png" "$APPDIR/chrome-bridge-for-ai.png"
cat > "$APPDIR/AppRun" <<'EOF'
#!/usr/bin/env bash
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/chrome-bridge-for-ai" "$@"
EOF
chmod -R a+rX "$APPDIR"
chmod 755 "$APPDIR/usr/bin/chrome-bridge-for-ai" "$APPDIR/AppRun"

if [ -x "$APPIMAGETOOL" ]; then
  ARCH=x86_64 "$APPIMAGETOOL" --no-appstream "$APPDIR" "$DIST_DIR/Chrome-Bridge-for-AI-x86_64.AppImage"
  chmod +x "$DIST_DIR/Chrome-Bridge-for-AI-x86_64.AppImage"
else
  echo "ERROR: appimagetool not found at $APPIMAGETOOL" >&2
  exit 1
fi

echo "Building Windows executable (amd64)..."
(cd "$ROOT_DIR" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags="-s -w -H windowsgui" -o "$DIST_DIR/chrome-bridge-for-ai-windows-amd64.exe" .)

echo "Building macOS Apple Silicon (arm64) executable..."
(cd "$ROOT_DIR" && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags="-s -w" -o "$DIST_DIR/chrome-bridge-for-ai-macos-apple-silicon-arm64" .)
chmod 755 "$DIST_DIR/chrome-bridge-for-ai-macos-apple-silicon-arm64"

echo "Building macOS Intel (amd64) executable..."
(cd "$ROOT_DIR" && GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags="-s -w" -o "$DIST_DIR/chrome-bridge-for-ai-macos-intel-amd64" .)
chmod 755 "$DIST_DIR/chrome-bridge-for-ai-macos-intel-amd64"

echo "Build artifacts:"
ls -lh "$DIST_DIR"
