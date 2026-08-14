#!/usr/bin/env bash
#
# build-linux.sh
#
# Build the PathfinderSSH front ends natively on Linux.
#
# Place this at the repo root (same directory as go.mod and cmd/).
# Run:   ./build-linux.sh                        # every front end under cmd/
#        TARGETS="pathfinder pfvault" ./build-linux.sh
#        TARGETS=gui ./build-linux.sh            # only the Fyne front ends
#        TARGETS=cli ./build-linux.sh            # only the console tools
#        STRIP=0 ./build-linux.sh                # keep symbols (debugging / delve)
#        RACE=1  ./build-linux.sh                # race build -> dist/linux/<app>-race
#        STATIC_CLI=1 ./build-linux.sh           # console tools with CGO off
#        VERSION=v0.93 ./build-linux.sh          # override the stamped version
#        TAGS="paid" ./build-linux.sh            # pass build tags through
#
# Build deps (Debian/Ubuntu):
#   sudo apt install -y gcc pkg-config libgl1-mesa-dev xorg-dev libxkbcommon-dev
# Fedora:  sudo dnf install gcc pkgconf-pkg-config mesa-libGL-devel libXcursor-devel \
#                           libXrandr-devel libXinerama-devel libXi-devel libxkbcommon-devel
#
# TARGET DISCOVERY: every directory under cmd/ that holds Go source is a target,
# so adding or removing a front end needs no edit here.
#
# GUI vs CLI: decided by whether the binary's dependency graph reaches
# fyne.io/fyne/v2/app -- that import is the only thing that pulls glfw and cgo.
# It costs a `go list` per target and it cannot go stale the way a hand-kept
# list can. On Linux the distinction only matters for STATIC_CLI; on Windows it
# decides -H windowsgui, which is why the same rule lives in all three scripts.
#
# VERSION: stamped with -X main.version=<git describe>. The linker silently
# ignores -X for a package with no such symbol, so it is safe to pass to every
# target even though only cmd/pathfinder reads it today (Help > About).
#
# RACE=1 notes:
#   - Costs roughly 5-10x CPU and memory. Debug only, never ship it.
#   - Output goes to a separate filename so a race binary can't be released
#     by accident, and STRIP is forced off (the detector needs symbols to
#     name the goroutines in its report).
#   - -trimpath is dropped for race builds so the report's stack frames carry
#     full source paths that match the working tree.
#   - Run it as:
#       GORACE="history_size=4 log_path=/tmp/pf-race" ./dist/linux/pathfinder-race
set -euo pipefail
cd "$(dirname "$0")"

OUT="dist/linux"
mkdir -p "$OUT"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
TAGS="${TAGS:-}"
SUFFIX=""

BUILDFLAGS=(-trimpath)
if [ "${RACE:-0}" = "1" ]; then
  BUILDFLAGS=(-race)   # replaces -trimpath: keep real paths in race reports
  SUFFIX="-race"
  STRIP=0
  echo ">> RACE BUILD: ~5-10x slower, do not ship these binaries"
fi
if [ -n "$TAGS" ]; then
  BUILDFLAGS=("${BUILDFLAGS[@]}" -tags "$TAGS")
fi

LDFLAGS="-X main.version=${VERSION}"
if [ "${STRIP:-1}" = "1" ]; then
  LDFLAGS="-s -w ${LDFLAGS}"   # strip symbol table + DWARF for a smaller release binary
fi

# --- target discovery -------------------------------------------------------

ALL=""
for d in cmd/*/ ; do
  name="${d#cmd/}"
  name="${name%/}"
  # skip a directory with no Go source in it
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

echo ">> version ${VERSION}, arch $(go env GOARCH)"

FAILED=""
for app in $LIST; do
  if [ ! -d "cmd/${app}" ]; then
    echo "!! no such target: cmd/${app}" >&2
    FAILED="${FAILED} ${app}"
    continue
  fi

  cgo=1
  kind="gui"
  if ! is_gui "$app"; then
    kind="cli"
    if [ "${STATIC_CLI:-0}" = "1" ]; then cgo=0; fi
  fi

  echo ">> building ${app}${SUFFIX} (${kind}, CGO=${cgo})"
  if CGO_ENABLED="${cgo}" GOOS=linux \
       go build "${BUILDFLAGS[@]}" -ldflags "${LDFLAGS}" \
       -o "${OUT}/${app}${SUFFIX}" "./cmd/${app}" ; then
    :
  else
    echo "!! FAILED: ${app}" >&2
    FAILED="${FAILED} ${app}"
  fi
done

echo ">> done: ${OUT}"
ls -lh "${OUT}"

if [ -n "${FAILED// /}" ]; then
  echo ">> FAILED:${FAILED}" >&2
  exit 1
fi

if [ "${RACE:-0}" = "1" ]; then
  echo ">> run with: GORACE=\"history_size=4 log_path=/tmp/pf-race\" ${OUT}/<app>-race"
fi