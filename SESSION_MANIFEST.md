# Session changes — 2026-08-01

Extract over the repo root. Contains **only** files changed this session.
No `go.mod` or `go.sum` — `go mod tidy` on your machine stays the authority.

`test.sh` is **not** included: the FLOOR_EXCLUDES change is already in your tree.

## Capture GUI work

    internal/capturerun/run.go          OnChange -> locked method; Sorted;
                                        RowsByState; Row.Display; Row.Path;
                                        Notable() renamed Decisions()
    internal/capturerun/event.go        Event.Path
    internal/capturerun/demo.go         NEW  DemoEvents() (data) + Demo() (playback)
    internal/capturerun/view_test.go    NEW  sorting, filtering, Path, Display,
                                        OnChange, demo determinism + pacing
    internal/capturerun/run_test.go     updated for the two renames

    internal/capture/browse.go          NEW  Browser iface (Devices/Types/
                                        History/Read), TypeInfo,
                                        UnreadableDevices, path-element guard
    internal/capture/browse_test.go     NEW  incl. LastSeen-on-unchanged
    internal/capture/demo_guard_test.go NEW  demo Kind set must be a subset of
                                        the engine's, driven against fakedev
    internal/capture/capture.go         Artifact.Path carried on the event
    internal/capture/store.go           Devices() moved to browse.go;
                                        History() guards its type element
    internal/capture/store_test.go      updated for []DeviceInfo

    cmd/capture/main.go                 Notable() -> Decisions()
    cmd/captureui/main.go               NEW  -demo / -store-only / live capture

    internal/ui/captureview.go          NEW  Run tab + Store tab

## internal/ui dead-code cull

15 unreferenced identifiers removed, 2 restored as unfinished-not-dead.
Package 8,581 -> 8,284 lines.

    internal/ui/terminal_widget.go        1885 -> 1759
    internal/ui/terminal_events.go         579 ->  466
    internal/ui/terminal_display.go        718 ->  675
    internal/ui/terminal_paste.go          334 ->  302
    internal/ui/terminal_theme_scope.go    169 ->  135
    internal/ui/theme_registry.go          386 ->  382

Restored with NOT WIRED doc comments: `extractWindowTitle` (its own Deferred
entry names the tabbed shell as the trigger) and
`updateAltScreenSelectionOverlay` (on pfterm's manual checklist).
Bracketed paste was never touched.

    Deferred.md                         new "## internal/capture" section:
                                        no per-type summary beside history.jsonl

## Verified

Non-Fyne tree: build, vet, gofmt, `go test ./...`, `-race` all clean on the
container floor recipe. capture 81.8%, capturerun 89.5%, capturedial 66.7%.

`internal/ui` and `cmd/captureui` cannot be compiled here (Fyne unreachable) —
verified by gofmt, AST parse, duplicate-declaration scan and unused-import scan
only. `./test.sh` is the real check.
