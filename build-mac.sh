#!/usr/bin/env bash
#
# build-macos.sh
#
# Build the PathfinderSSH front ends natively on macOS (run this ON a Mac).
#
# Place this at the repo root (same directory as go.mod and cmd/).
# Run:   ./build-macos.sh                        # host arch, every front end
#        ./build-macos.sh universal              # fat binaries: arm64 + amd64 via lipo
#        ./build-macos.sh amd64                  # force one arch
#        TARGETS="pathfinder pfvault" ./build-macos.sh
#        TARGETS=gui ./build-macos.sh universal  # only the Fyne front ends
#        TARGETS=cli ./build-macos.sh            # only the console tools
#        STRIP=0 ./build-macos.sh                # keep symbols
#        STATIC_CLI=1 ./build-macos.sh           # console tools with CGO off
#        VERSION=v0.93 ./build-macos.sh          # override the stamped version
#        TAGS="paid" ./build-macos.sh            # pass build tags through
#
# Build deps: Xcode command line tools ->  xcode-select --install
#
# Cross note: Apple's clang is multi-arch, so on either Apple Silicon or Intel you
# can build BOTH darwin arches locally; "universal" just builds each and lipo's them.
#
# TARGET DISCOVERY: every directory under cmd/ that holds Go source is a target,
# so adding or removing a front end needs no edit here.
#
# GUI vs CLI: decided by whether the binary's dependency graph reaches
# fyne.io/fyne/v2/app -- that import is the only thing that pulls glfw and cgo.
# Same rule in all three build scripts.
#
# VERSION: stamped with -X main.version=<git describe>. The linker silently
# ignores -X for a package with no such symbol, so it is safe to pass to every
# target even though only cmd/pathfinder reads it today (Help > About).
set -euo pipefail
cd "$(dirname "$0")"

OUT="dist/macos"
mkdir -p "$OUT"

ARCH="${1:-$(go env GOARCH)}"   # arm64 | amd64 | universal

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.93)}"
TAGS="${TAGS:-}"

BUILDFLAGS=(-trimpath)
if [ -n "$TAGS" ]; then
  BUILDFLAGS=("${BUILDFLAGS[@]}" -tags "$TAGS")
fi

LDFLAGS="-X main.version=${VERSION}"
if [ "${STRIP:-1}" = "1" ]; then
  LDFLAGS="-s -w ${LDFLAGS}"
fi

# --- target discovery -------------------------------------------------------

ALL=""
for d in cmd/*/ ; do
  name="${d#cmd/}"
  name="${name%/}"
  ls "${d}"*.go >/dev/null 2>&1 || continue
  ALL="${ALL} ${name}"
done

if [ -z "${ALL// /}" ]; then
  echo "!! no buildable directories under cmd/ -- is this the repo root?" >&2
  exit 1
fi

is_gui () {
  go list -deps "./cmd/$1" 2>/dev/null | grep -qx 'fyne.io/fyne/v2/app'
}

SELECT="${TARGETS:-all}"
case "$SELECT" in
  all) LIST="$ALL" ;;
  gui) LIST=""
       for t in $ALL; do
         if is_gui "$t"; then LIST="${LIST} ${t}"; fi
       done ;;
  cli) LIST=""
       for t in $ALL; do
         if ! is_gui "$t"; then LIST="${LIST} ${t}"; fi
       done ;;
  *)   LIST="$SELECT" ;;
esac

if [ -z "${LIST// /}" ]; then
  echo "!! nothing selected (TARGETS=${SELECT})" >&2
  exit 1
fi

# --- build ------------------------------------------------------------------

# $1 app, $2 goarch, $3 output path, $4 cgo
build_one () {
  local app="$1" goarch="$2" out="$3" cgo="$4"
  echo ">> building ${app} for darwin/${goarch} (CGO=${cgo})"
  CGO_ENABLED="${cgo}" GOOS=darwin GOARCH="${goarch}" \
    go build "${BUILDFLAGS[@]}" -ldflags "${LDFLAGS}" -o "${out}" "./cmd/${app}"
}

echo ">> version ${VERSION}, arch ${ARCH}"

FAILED=""
for app in $LIST; do
  if [ ! -d "cmd/${app}" ]; then
    echo "!! no such target: cmd/${app}" >&2
    FAILED="${FAILED} ${app}"
    continue
  fi

  cgo=1
  if ! is_gui "$app"; then
    if [ "${STATIC_CLI:-0}" = "1" ]; then cgo=0; fi
  fi

  ok=1
  if [ "${ARCH}" = "universal" ]; then
    build_one "${app}" arm64 "${OUT}/${app}.arm64" "${cgo}" || ok=0
    if [ "${ok}" = "1" ]; then
      build_one "${app}" amd64 "${OUT}/${app}.amd64" "${cgo}" || ok=0
    fi
    if [ "${ok}" = "1" ]; then
      echo ">> lipo -> ${app}"
      lipo -create -output "${OUT}/${app}" "${OUT}/${app}.arm64" "${OUT}/${app}.amd64" || ok=0
    fi
    rm -f "${OUT}/${app}.arm64" "${OUT}/${app}.amd64"
  else
    build_one "${app}" "${ARCH}" "${OUT}/${app}" "${cgo}" || ok=0
  fi

  if [ "${ok}" != "1" ]; then
    echo "!! FAILED: ${app}" >&2
    FAILED="${FAILED} ${app}"
  fi
done

echo ">> done: ${OUT}"
for app in $LIST; do
  if [ -f "${OUT}/${app}" ]; then file "${OUT}/${app}"; fi
done

if [ -n "${FAILED// /}" ]; then
  echo ">> FAILED:${FAILED}" >&2
  exit 1
fi

# Optional: produce a double-clickable PathfinderSSH.app instead of a bare binary.
#   go install fyne.io/fyne/v2/cmd/fyne@latest
#   fyne package -os darwin -name PathfinderSSH -icon Icon.png -src ./cmd/pathfinder
# (run from repo root; needs an Icon.png and FyneApp.toml or -appID set)