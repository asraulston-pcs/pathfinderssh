#!/usr/bin/env bash
#
# package-macos.sh
#
# Wrap a built pathfinder binary in a double-clickable PathfinderSSH.app.
#
# Place this at the repo root (same directory as go.mod and cmd/).
# Run:   ./package-macos.sh                     # wrap dist/macos/pathfinder
#        BIN=dist/macos/pathfinder ./package-macos.sh
#        ICON=path/to/square.png ./package-macos.sh
#        VERSION=v0.93 ./package-macos.sh       # override the stamped version
#        SIGN=0 ./package-macos.sh              # skip the ad-hoc signature
#
# This does NOT build. Run ./build-macos.sh first (or "./build-macos.sh
# universal" for a fat binary) and this wraps whatever is there, so the app and
# the CLI tools always come from the same build.
#
# WHY HAND-ROLLED rather than "fyne package": that command wants FyneApp.toml
# or -appID and re-builds the binary itself, which would bypass build-macos.sh
# and its version stamping. A bundle is a directory with a plist in it; this
# keeps one build path.
#
# NOTE ON THE APP ID: CFBundleIdentifier here is macOS metadata only. Fyne's
# Preferences API keys off the ID passed to app.NewWithID in the Go code, not
# off this plist -- setting one does not enable the other.
#
# ICON: defaults to the About-box logo. That artwork is portrait, and an app
# icon must be square, so it is scaled to fit and then PADDED rather than
# stretched. Pass ICON= to use purpose-made square art instead. iconutil and
# sips are both stock macOS; if either is missing the bundle is still built,
# just with the generic icon.
set -euo pipefail
cd "$(dirname "$0")"

APP_NAME="PathfinderSSH"
BUNDLE_ID="com.scottpeterman.pathfinderssh"
OUT="dist/macos"
BIN="${BIN:-${OUT}/pathfinder}"
ICON="${ICON:-internal/ui/assets/pathfinderlogo.png}"
PAD_COLOR="${PAD_COLOR:-000000}"   # the logo is dark-background artwork
MIN_MACOS="${MIN_MACOS:-11.0}"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
# CFBundleShortVersionString must be dot-separated digits or Finder shows
# nothing; a git describe like v0.9.3-2-gabc123 is fine for CFBundleVersion but
# not for the display string, so strip it back to the numeric core.
SHORT_VERSION="$(printf '%s' "$VERSION" | sed -e 's/^v//' -e 's/[^0-9.].*$//' -e 's/\.$//')"
if [ -z "$SHORT_VERSION" ]; then SHORT_VERSION="0.0.0"; fi

if [ ! -f "$BIN" ]; then
  echo "!! no binary at ${BIN} -- run ./build-macos.sh first" >&2
  exit 1
fi

APP="${OUT}/${APP_NAME}.app"
echo ">> packaging ${APP}  (version ${VERSION}, short ${SHORT_VERSION})"

# A stale bundle is worse than none: an old binary inside a fresh-looking .app
# is the same trap as a stale build artifact.
rm -rf "$APP"
mkdir -p "${APP}/Contents/MacOS" "${APP}/Contents/Resources"

cp "$BIN" "${APP}/Contents/MacOS/${APP_NAME}"
chmod +x "${APP}/Contents/MacOS/${APP_NAME}"

# --- icon ------------------------------------------------------------------

ICON_KEY=""
if [ -f "$ICON" ] && command -v sips >/dev/null 2>&1 && command -v iconutil >/dev/null 2>&1; then
  ICONSET="$(mktemp -d)/${APP_NAME}.iconset"
  mkdir -p "$ICONSET"
  SQUARE="$(dirname "$ICONSET")/square.png"

  # Fit inside 1024 (aspect preserved), then pad out to a square canvas.
  sips -Z 1024 "$ICON" --out "$SQUARE" >/dev/null 2>&1
  sips "$SQUARE" --padToHeightWidth 1024 1024 --padColor "$PAD_COLOR" \
       --out "$SQUARE" >/dev/null 2>&1

  ok=1
  for sz in 16 32 128 256 512; do
    sips -z "$sz" "$sz" "$SQUARE" --out "${ICONSET}/icon_${sz}x${sz}.png" >/dev/null 2>&1 || ok=0
    sips -z "$((sz * 2))" "$((sz * 2))" "$SQUARE" \
         --out "${ICONSET}/icon_${sz}x${sz}@2x.png" >/dev/null 2>&1 || ok=0
  done

  if [ "$ok" = "1" ] && iconutil -c icns "$ICONSET" -o "${APP}/Contents/Resources/${APP_NAME}.icns" 2>/dev/null; then
    ICON_KEY="$APP_NAME"
    echo ">> icon built from ${ICON}"
  else
    echo ">> icon step failed -- bundling without one"
  fi
else
  echo ">> no icon (missing ${ICON}, sips or iconutil) -- bundling without one"
fi

# --- Info.plist ------------------------------------------------------------

{
  echo '<?xml version="1.0" encoding="UTF-8"?>'
  echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
  echo '<plist version="1.0">'
  echo '<dict>'
  echo "  <key>CFBundleName</key><string>${APP_NAME}</string>"
  echo "  <key>CFBundleDisplayName</key><string>${APP_NAME}</string>"
  echo "  <key>CFBundleExecutable</key><string>${APP_NAME}</string>"
  echo "  <key>CFBundleIdentifier</key><string>${BUNDLE_ID}</string>"
  echo '  <key>CFBundlePackageType</key><string>APPL</string>'
  echo '  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>'
  echo "  <key>CFBundleShortVersionString</key><string>${SHORT_VERSION}</string>"
  echo "  <key>CFBundleVersion</key><string>${VERSION}</string>"
  if [ -n "$ICON_KEY" ]; then
    echo "  <key>CFBundleIconFile</key><string>${ICON_KEY}</string>"
  fi
  echo "  <key>LSMinimumSystemVersion</key><string>${MIN_MACOS}</string>"
  echo '  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>'
  echo '  <key>NSHighResolutionCapable</key><true/>'
  echo '</dict>'
  echo '</plist>'
} > "${APP}/Contents/Info.plist"

printf 'APPL????' > "${APP}/Contents/PkgInfo"

# --- signature -------------------------------------------------------------

# Ad-hoc only. Copying a Go binary into a bundle invalidates the signature the
# linker applies on arm64, and an invalid signature is worse than none -- macOS
# refuses to launch it. This re-signs in place; it is NOT distribution signing.
if [ "${SIGN:-1}" = "1" ] && command -v codesign >/dev/null 2>&1; then
  if codesign --force --sign - --timestamp=none "$APP" 2>/dev/null; then
    echo ">> ad-hoc signed"
  else
    echo ">> codesign failed -- the app may not launch on Apple Silicon"
  fi
fi

echo ">> done: ${APP}"
echo ">> try it:   open ${APP}"
echo ">> install:  cp -R ${APP} /Applications/"