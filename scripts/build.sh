#!/usr/bin/env bash
# Builds the web UI and every desktop/server binary into dist/.
#   scripts/build.sh            → web UI + Windows + Linux
#   scripts/build.sh macos      → macOS .app (run on a Mac; needs cgo for the menu-bar icon)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=${VERSION:-3.1.0}
LDFLAGS="-s -w -X main.version=$VERSION"
mkdir -p dist

if [ "${SKIP_WEB:-}" != "1" ]; then
  echo "› Web UI"
  (cd webui && (npm ci --no-audit --no-fund || npm install --no-audit --no-fund) && npm run build)
fi

if [ "${1:-}" = "macos" ]; then
  echo "› macOS"
  for arch in arm64 amd64; do
    CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "dist/ferry-darwin-$arch" .
  done
  APP=dist/Ferry.app
  rm -rf "$APP" && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  lipo -create -output "$APP/Contents/MacOS/Ferry" dist/ferry-darwin-arm64 dist/ferry-darwin-amd64
  sed "s/VERSION/$VERSION/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"
  ICONSET=$(mktemp -d)/Ferry.iconset && mkdir -p "$ICONSET"
  for s in 16 32 128 256 512; do
    sips -z $s $s assets/icon-1024.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
    sips -z $((s*2)) $((s*2)) assets/icon-1024.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
  done
  iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/Ferry.icns"
  codesign --force --deep -s - "$APP" || true   # ad-hoc signature
  (cd dist && ditto -c -k --keepParent Ferry.app "Ferry-$VERSION-macOS.zip")
  rm -f dist/ferry-darwin-*
  exit 0
fi

echo "› Windows"
RSRC=${RSRC:-rsrc}
for arch in amd64 arm64; do
  "$RSRC" -arch $arch -ico assets/icon.ico -manifest packaging/windows/ferry.manifest -o rsrc_windows_$arch.syso
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "dist/Ferry-$VERSION-windows-$( [ $arch = amd64 ] && echo x64 || echo arm64 ).exe" .
  rm -f rsrc_windows_$arch.syso
done

echo "› Linux"
for arch in amd64 arm64; do
  name="Ferry-$VERSION-linux-$( [ $arch = amd64 ] && echo x64 || echo arm64 )"
  tmp=$(mktemp -d)/ferry && mkdir -p "$tmp"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$tmp/ferry" .
  cp assets/icon.png "$tmp/ferry.png"
  cp packaging/linux/ferry.desktop packaging/linux/install.sh "$tmp/"
  tar -C "$(dirname "$tmp")" -czf "dist/$name.tar.gz" ferry
done
echo "✓ dist/"
