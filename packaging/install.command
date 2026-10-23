#!/bin/bash
# Installs cantevenReMark into Applications.
# Run it from Terminal:  bash install.command
# (It clears the "downloaded from the internet" flag and signs the app for this Mac,
#  because the app isn't notarized by Apple.)
set -e
cd "$(dirname "$0")"
SRC="cantevenReMark.app"
[ -d "$SRC" ] || { echo "Put this file next to cantevenReMark.app"; exit 1; }
DEST="/Applications/cantevenReMark.app"
[ -w /Applications ] || DEST="$HOME/Applications/cantevenReMark.app"
mkdir -p "$(dirname "$DEST")"
osascript -e 'tell application id "com.husambunni.cantevenremark" to quit' 2>/dev/null || true
rm -rf "$DEST" "$(dirname "$DEST")/CantEvenRemark.app"
cp -R "$SRC" "$DEST"
xattr -cr "$DEST"
codesign --force --deep --sign - "$DEST"
echo "Installed to $DEST"
open "$DEST"
