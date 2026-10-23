#!/bin/bash
# Builds dist/CantEvenRemark.app (and a .zip of it).
# Works on macOS (universal binary + ad-hoc signature) or Linux (Apple Silicon only).
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=$(grep -m1 'var version' main.go | sed -E 's/.*"(.*)".*/\1/')
APP=dist/cantevenReMark.app
rm -rf dist && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

build() { CGO_ENABLED=0 GOOS=darwin GOARCH=$1 go build -trimpath -ldflags="-s -w" -o "$2" .; }
if [[ "$(uname)" == "Darwin" ]] && command -v lipo >/dev/null; then
  build arm64 dist/cer-arm64; build amd64 dist/cer-amd64
  lipo -create -output "$APP/Contents/MacOS/cantevenremark" dist/cer-arm64 dist/cer-amd64
  rm dist/cer-arm64 dist/cer-amd64
else
  build arm64 "$APP/Contents/MacOS/cantevenremark"
fi

sed "s/__VERSION__/$VERSION/g" packaging/Info.plist > "$APP/Contents/Info.plist"
printf 'APPL????' > "$APP/Contents/PkgInfo"
cp packaging/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
chmod +x "$APP/Contents/MacOS/cantevenremark"

if command -v codesign >/dev/null; then
  codesign --force --deep --sign - "$APP"
fi
(cd dist && zip -qry cantevenReMark.zip cantevenReMark.app)
echo "built $APP (v$VERSION)"
