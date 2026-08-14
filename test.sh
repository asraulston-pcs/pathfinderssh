#!/usr/bin/env bash
# scripts/test.sh
#
# Runs the full check suite: build, gofmt, vet, tests.
#
# Locates the module root by walking up from its own location looking for
# go.mod, so it works from the repo root, from scripts/, or from anywhere.
#
# Usage:
#   ./test.sh                    # build + gofmt + vet + tests
#   ./test.sh -v                 # verbose test output
#   ./test.sh -r                 # with the race detector
#   ./test.sh -c                 # with coverage, prints per-package totals
#   ./test.sh -f                 # fix formatting instead of just reporting it
#   ./test.sh -p ./internal/jump/...   # limit to one package
#   ./test.sh -n 5               # run tests 5 times (flake hunting)
#   ./test.sh -x scratch -x docs # exclude more directories (repeatable)
#   ./test.sh -F                 # also check against scripts/floor.deps
#
# Portable to bash 3.2, which is what macOS ships — no mapfile, no namerefs,
# and no bare expansion of a possibly-empty array under `set -u`.
#
# POC, vendor, testdata, and dot-directories are excluded by default. POC holds
# original code and examples that are not part of the build and should not be
# reformatted or vetted.
#
# FLOOR_EXCLUDES (below) is separate: those directories build, vet and test
# normally and are only left out of -F, because their dependencies have a newer
# floor than the module does.
#
# Exit status is non-zero if any stage fails, so it works as a pre-commit hook
# or a CI step unchanged.

set -euo pipefail

# mktemp -t means "prefix" on BSD and "template" on GNU, so the same invocation
# produces pathfinder-cover.XXXXXX.a1b2c3 on macOS and a proper temp name on
# Linux. Giving a full path with the X's in it behaves identically on both.
TMPBASE="${TMPDIR:-/tmp}"
TMPBASE="${TMPBASE%/}"

# Find the module root: walk up from this script looking for go.mod. Assuming a
# fixed depth is how this ends up testing whatever happens to sit in the parent
# directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR"
while [ "$ROOT" != "/" ] && [ ! -f "$ROOT/go.mod" ]; do
  ROOT="$(dirname "$ROOT")"
done
if [ ! -f "$ROOT/go.mod" ]; then
  echo "no go.mod found at or above $SCRIPT_DIR" >&2
  exit 1
fi
cd "$ROOT"

PKG="./..."
VERBOSE=""
RACE=""
COVER=""
FIXFMT=0
COUNT=""
SHORT=""
EXPLICIT_PKG=0
FLOOR=0

# Directories that are never built, vetted, formatted, or tested. POC is
# original code and examples kept for reference, not part of the module.
EXCLUDES=(POC vendor testdata)

# Directories dropped from the floor copy only. They are built, vetted and
# tested normally; they are just not part of the oldest-supported-dependency
# check. The GUI is the case this exists for: Fyne brings ~40 transitive
# modules with their own, newer floor, so pinning them in floor.deps would
# exercise a combination the shipping tree never uses. The GUI is validated by
# building and running it, which this script cannot do anyway -- it needs cgo,
# a C toolchain and a display.
FLOOR_EXCLUDES=(internal/ui cmd/pfterm cmd/crawlui cmd/captureui cmd/pfconnect cmd/pathfinder)

while getopts "vrcfsFp:n:x:h" opt; do
  case "$opt" in
    v) VERBOSE="-v" ;;
    r) RACE="-race" ;;
    c) COVER=1 ;;
    f) FIXFMT=1 ;;
    s) SHORT="-short" ;;
    p) PKG="$OPTARG"; EXPLICIT_PKG=1 ;;
    x) EXCLUDES+=("$OPTARG") ;;
    F) FLOOR=1 ;;
    n) COUNT="-count=$OPTARG" ;;
    h) awk 'NR>1 && /^#/ {sub(/^# ?/,""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    *) echo "try: $0 -h" >&2; exit 2 ;;
  esac
done

# Colour only when attached to a terminal, so CI logs stay clean.
if [ -t 1 ]; then
  BOLD=$'\033[1m'; RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RESET=$'\033[0m'
else
  BOLD=""; RED=""; GREEN=""; YELLOW=""; RESET=""
fi

FAILED=()

stage() { printf '%s==> %s%s\n' "$BOLD" "$1" "$RESET"; }
ok()    { printf '    %sok%s      %s\n' "$GREEN" "$RESET" "$1"; }
bad()   { printf '    %sFAILED%s  %s\n' "$RED" "$RESET" "$1"; FAILED+=("$1"); }
note()  { printf '    %s%s%s\n' "$YELLOW" "$1" "$RESET"; }

if ! command -v go >/dev/null 2>&1; then
  echo "${RED}go is not on PATH${RESET}" >&2
  exit 1
fi

MODULE="$(go list -m 2>/dev/null || echo "?")"
printf '%s%s%s\n' "$BOLD" "$(go version)" "$RESET"
printf 'module %s\n   root %s\n' "$MODULE" "$(pwd)"

# A path segment regex matching any excluded directory, e.g. "/(POC|vendor)/".
EXCL_RE="/($(IFS='|'; echo "${EXCLUDES[*]}"))(/|$)"

# PKGS is what every go command operates on. Nested modules are already
# invisible to ./... , but a plain subdirectory is not, so filter explicitly.
# list_pkgs PATTERN -> package import paths, minus the exclusions.
list_pkgs() {
  go list "$1" 2>/dev/null | grep -Ev "$EXCL_RE" || true
}

# read_pkgs ARRAYNAME PATTERN — fill an array with package paths.
#
# This is what mapfile does in one line, but macOS ships bash 3.2 (the last
# GPLv2 release) and mapfile arrived in bash 4. eval is doing the indirect
# assignment because bash 3.2 has no namerefs either; the input is go list
# output filtered by our own regex, not anything user-supplied.
read_pkgs() {
  local __name="$1" __pattern="$2" __line
  eval "$__name=()"
  while IFS= read -r __line; do
    [ -n "$__line" ] && eval "$__name+=(\"\$__line\")"
  done < <(list_pkgs "$__pattern")
}

declare -a PKGS
MAIN_OK=1
if [ "$EXPLICIT_PKG" -eq 1 ]; then
  PKGS=("$PKG")
  printf '\n'
else
  printf 'excluding %s\n\n' "${EXCLUDES[*]}"
  read_pkgs PKGS ./...
  if [ ${#PKGS[@]} -eq 0 ]; then
    # Either everything is excluded, or the local toolchain is older than this
    # module's go directive. The second case is exactly what -F is for, so it
    # is a skip rather than a hard stop.
    LISTERR="$(go list ./... 2>&1 >/dev/null | head -2 || true)"
    MAIN_OK=0
    note "cannot list packages (toolchain older than the go directive, or everything excluded):"
    printf '      %s\n' "$LISTERR"
  fi
fi

skip() { printf '    %sskipped%s %s\n' "$YELLOW" "$RESET" "$1"; }

# ---------------------------------------------------------------------------
stage "build"
if [ "$MAIN_OK" -eq 0 ]; then skip "build"
elif go build "${PKGS[@]}"; then ok "build"; else bad "build"; fi

# ---------------------------------------------------------------------------
stage "gofmt"
# gofmt walks the filesystem and knows nothing about modules or packages, so
# prune the excluded directories (and dot-directories) here directly.
declare -a PRUNE
for d in "${EXCLUDES[@]}"; do PRUNE+=(-path "./$d" -prune -o); done
UNFORMATTED="$(find . "${PRUNE[@]}" -name '.?*' -prune -o -name '*.go' \
  -exec gofmt -l {} + 2>/dev/null || true)"
if [ -z "$UNFORMATTED" ]; then
  ok "gofmt"
elif [ "$FIXFMT" -eq 1 ]; then
  echo "$UNFORMATTED" | xargs gofmt -w
  note "reformatted:"
  echo "$UNFORMATTED" | sed 's/^/      /'
  ok "gofmt (fixed)"
else
  bad "gofmt"
  note "not formatted (re-run with -f to fix):"
  echo "$UNFORMATTED" | sed 's/^/      /'
fi

# ---------------------------------------------------------------------------
stage "vet"
if [ "$MAIN_OK" -eq 0 ]; then skip "vet"
elif go vet "${PKGS[@]}"; then ok "vet"; else bad "vet"; fi

# ---------------------------------------------------------------------------
stage "test"
COVERFLAGS=()
COVERFILE=""
if [ "$MAIN_OK" -eq 0 ]; then
  skip "test"
  COVER=""
elif [ -n "$COVER" ]; then
  COVERFILE="$(mktemp "$TMPBASE/pathfinder-cover.XXXXXX")"
  COVERFLAGS=(-coverprofile="$COVERFILE" -covermode=atomic)
fi

# shellcheck disable=SC2086
if [ "$MAIN_OK" -eq 1 ]; then
  # ${A[@]+"${A[@]}"} rather than "${A[@]}": expanding an empty array under
  # `set -u` is an unbound-variable error in bash 3.2, and COVERFLAGS is empty
  # on every run without -c.
  if go test $VERBOSE $RACE $SHORT $COUNT ${COVERFLAGS[@]+"${COVERFLAGS[@]}"} "${PKGS[@]}"; then
    ok "test"
  else
    bad "test"
  fi
fi

if [ -n "$COVER" ] && [ -s "$COVERFILE" ]; then
  echo
  stage "coverage"
  go tool cover -func="$COVERFILE" | tail -1 | sed 's/^/    /'
  note "html report: go tool cover -html=$COVERFILE"
else
  [ -n "$COVERFILE" ] && rm -f "$COVERFILE"
fi

# ---------------------------------------------------------------------------
# Floor check: rebuild and retest against the oldest supported dependency set,
# in a throwaway copy so the working tree keeps the versions it ships with.
FLOORFILE="$ROOT/scripts/floor.deps"
if [ "$FLOOR" -eq 1 ]; then
  echo
  stage "floor"
  if [ ! -f "$FLOORFILE" ]; then
    bad "floor (no scripts/floor.deps)"
  else
    TMP="$(mktemp -d "$TMPBASE/pathfinder-floor.XXXXXX")"
    tar -cf - --exclude=.git --exclude=POC . | (cd "$TMP" && tar -xf -)
    if (
      cd "$TMP"
      # Never let the go directive trigger a toolchain download here: the whole
      # point is to run the floor set on whatever toolchain is present. The
      # directive is lowered by the first floor.deps entry.
      export GOTOOLCHAIN=local
      # Throwaway copy, so the floor exclusions are simply deleted rather than
      # filtered out of every later command.
      if [ ${#FLOOR_EXCLUDES[@]} -gt 0 ]; then
        for d in "${FLOOR_EXCLUDES[@]}"; do rm -rf "./$d"; done
      fi
      rm -f go.sum
      while read -r mod ver; do
        case "$mod" in ""|\#*) continue ;; esac
        if [ "$mod" = "go" ]; then
          go mod edit -go="$ver"
        else
          go mod edit -require="$mod@$ver"
        fi
      done < "$FLOORFILE"
      # Optional local overrides, e.g. module mirrors where the default proxy
      # is unreachable. Not part of the repo.
      [ -f "$ROOT/scripts/floor.local" ] && cat "$ROOT/scripts/floor.local" >> go.mod
      go mod tidy >/dev/null 2>&1 || true
      read_pkgs FPKGS ./...
      [ ${#FPKGS[@]} -eq 0 ] && { echo "      no packages resolvable at floor versions"; exit 1; }
      go build "${FPKGS[@]}" 2>&1 | sed 's/^/      /'
      go test "${FPKGS[@]}" 2>&1 | sed 's/^/      /'
    ); then
      if [ ${#FLOOR_EXCLUDES[@]} -gt 0 ]; then
        note "floor excluded: ${FLOOR_EXCLUDES[*]}"
      fi
      ok "floor [$(grep -Ev '^\s*(#|$)' "$FLOORFILE" | tr '\n' ' ' | sed 's/ *$//')]"
    else
      bad "floor"
    fi
    rm -rf "$TMP"
  fi
fi

# ---------------------------------------------------------------------------
echo
if [ "$MAIN_OK" -eq 0 ] && [ "$FLOOR" -eq 0 ]; then
  printf '%s%snothing was checked%s - the local toolchain cannot build this module.\n' "$BOLD" "$RED" "$RESET"
  printf 'Upgrade go, or run with -F to check against %s\n' "scripts/floor.deps"
  exit 1
fi

if [ ${#FAILED[@]} -eq 0 ]; then
  if [ "$MAIN_OK" -eq 0 ]; then
    printf '%s%sfloor checks passed%s (the shipping dependency set was not checked here)\n' \
      "$BOLD" "$YELLOW" "$RESET"
  else
    printf '%s%sall checks passed%s\n' "$BOLD" "$GREEN" "$RESET"
  fi
  exit 0
fi

printf '%s%s%d stage(s) failed:%s %s\n' "$BOLD" "$RED" "${#FAILED[@]}" "$RESET" "${FAILED[*]}"
exit 1