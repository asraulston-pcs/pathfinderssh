# Fyne UI

Conventions and hard-won constraints for the desktop side. Fyne v2.6.2.

    internal/ui         widgets and views
    cmd/pathfinder      the shell — tabs, instances, detachable windows
    cmd/pfterm          the terminal harness
    cmd/pfconnect       the session-dialog harness
    cmd/crawlui         the crawl harness
    cmd/captureui       the capture harness

This is a working document rather than a tour. Everything in it is here
because getting it wrong produced a failure that named something other than
the cause, and those cost the most time.

A disproportionate amount of what follows is about **silence**. Fyne's failure
mode is rarely a panic or an error return; it is a call that does nothing and
reports success. Most of this document is a list of the places that happens and
what to check instead.

---

## Lifecycle

### `app.New()` comes first

Fyne resolves theme and driver through the current app. Constructing any widget
before one exists dereferences nil deep inside `Button.CreateRenderer`, and the
panic trace names a layout function:

    Attempt to access current Fyne app when none is started
    panic: runtime error: invalid memory address or nil pointer dereference
    fyne.io/fyne/v2/widget.(*Button).CreateRenderer
    fyne.io/fyne/v2/layout.hBoxLayout.Layout
    ...ui.(*CrawlView).build

Nothing in that says "you built a widget too early". So: `app.New()` and
`NewWindow` at the top of `main`, before any constructor that builds widgets.

Any constructor that builds widgets says so in its doc comment. `NewCrawlView`
does.

### Background work waits for the driver

`fyne.Do` hands work to the driver, and the driver does not exist until
`ShowAndRun`. A goroutine started in a constructor can easily fire before then
— a comparison loaded at startup sets a dirty flag immediately, and the redraw
ticker is a few milliseconds behind it.

The pattern: start the goroutine in the constructor, but gate it.

```go
func NewCrawlView(run *crawlrun.Run) *CrawlView {
    v := &CrawlView{started: make(chan struct{})}
    ...
    go v.redrawLoop()   // blocks on v.started
    return v
}

// Start releases the redraw loop. Call immediately before ShowAndRun.
func (v *CrawlView) Start() { ... close(v.started) ... }
```

The host calls `Start()` on the line before `ShowAndRun`.

Anything a view wants to do *at* startup goes behind that gate too, not in
`Start` itself. The capture view's first store read had to move into the redraw
loop's first tick: it ends in `fyne.Do`, and `Start` runs on the line before
`ShowAndRun`, so calling it there raced the driver into existence.

### Stopping

Long-running work gets a `context.Context` whose cancel the Stop button holds,
and `signal.Notify` wired to the same cancel so Ctrl-C behaves like the button
rather than killing the process mid-session.

A view that starts a goroutine gets a `Stop()` so the host can `defer` it.

**`Stop` is teardown, not pause.** It sets a flag the loop returns on, and the
loop does not come back. So it is the wrong verb for hiding a tab — and hiding
must not mean idle anyway: a backgrounded terminal has to keep reading its
socket and a backgrounded crawl has to keep aggregating. If visibility ever
needs to gate anything, the split is that a model's work loop ignores it and a
view's redraw loop obeys it.

---

## Threading

Emission comes from worker goroutines. Every widget call goes inside `fyne.Do`.

The pattern that works for a high-rate producer:

```go
run.OnChange(func() { v.dirty.Store(true) })   // called from crawl workers
```

```go
func (v *CrawlView) redrawLoop() {
    <-v.started
    t := time.NewTicker(200 * time.Millisecond)
    for range t.C {
        if !v.dirty.Swap(false) { continue }
        v.refresh()                            // computes off-thread
    }
}
```

and `refresh` does its reads and sorting first, then touches widgets only
inside a single `fyne.Do`.

**Coalesce.** A crawl emits thousands of events. Refreshing a table per event
spends the whole frame budget rendering states nobody can read at that rate. An
atomic flag plus a ticker is enough; there is no need for a channel.

**Snapshot, then render.** `Table.UpdateCell` is called for every visible cell
on every refresh, so it must be cheap and must not lock anything a worker
holds. Build a `[]DeviceRow` under the read lock once per refresh and have the
cell function index into it.

### One UI goroutine, all windows

Every window in the process shares one driver and one UI goroutine. Opening a
second window buys screen space, not parallelism, and the `fyne.Do` discipline
is unchanged by it. A slow paint in a detached window is a slow paint
everywhere.

---

## Moving a widget between windows

This is the longest section because it produced five consecutive failures, all
silent, and the underlying facts are not in the documentation. If you are
adding a second detachable surface, read this first.

The feature: pull a live SSH session out of the tab strip into a window of its
own without dropping the connection. `Content()` returns a `fyne.CanvasObject`
and the view never learns where it lives, so in principle a tab and a window
are the same thing and the move is a re-parent. In practice, four separate
mechanisms assume an object never moves.

### The object→canvas cache is write-once

`fyne.Driver.CanvasForObject` reads a map written by
`cache.SetCanvasForObject`, in `internal/cache/canvases.go`:

```go
old, found := canvases.LoadOrStore(obj, cinfo)
```

`LoadOrStore`. The entry records the **first** canvas an object rendered on and
is never updated. Rendering the same object on a second canvas keeps the
original. Entries die only by expiry, and every lookup and every render marks
them alive, so an on-screen object's entry never expires. There is no API to
correct it.

Everything below is a consequence of that one line.

### Focus goes to the window the widget used to be in

The terminal's `MouseDown` calls `GrabFocus`, which resolved its canvas through
the driver — so the first click inside a detached session handed focus back to
the window the session used to be in. The same lookup made a right-click open
its popup menu on the main window, behind the one the pointer was in.

The fix is an explicit override, and the shape generalises:

```go
// SetHostCanvas records the canvas this widget is currently displayed on,
// overriding the driver's cache. Pass nil to go back to the cache.
func (t *NativeTerminalWidget) SetHostCanvas(c fyne.Canvas) { ... }

func (t *NativeTerminalWidget) FocusCanvas() fyne.Canvas {
    if c := t.hostCanvas(); c != nil {
        return c
    }
    return fyne.CurrentApp().Driver().CanvasForObject(t.focusObject())
}
```

Every canvas or window lookup in the widget then routes through `FocusCanvas`
— focus, popups, dialogs — and the host calls `SetHostCanvas` on every move.
The shell does that through `Mount.OnCanvasChange`, so it hands over a canvas
and stays ignorant of what the applet does with it.

**`AllWindows()[0]` is the same bug with the main window hard-coded.** Grep for
it before adding a detachable surface. The one legitimate use is the clipboard,
which is the system's rather than the window's.

### Repaint goes there too

`canvas.Refresh(obj)`, in `canvas/canvas.go`, is the entry point for every
repaint request in the toolkit:

```go
c := app.Driver().CanvasForObject(obj)
if c != nil { c.Refresh(obj) }
```

Same lookup. So a moved applet marks its **original** canvas dirty on every
redraw and its own canvas never. The window then repaints only when something
else happens to dirty it.

The symptom was a selection dragged in a detached terminal staying invisible
until a popup menu was opened, at which point it appeared all at once — the
menu's objects were new, so they registered on the right canvas, dirtied it,
and the whole window caught up. It also looked like the terminal had stopped
accepting input, because keystrokes were reaching the device and the echo was
never painted.

The shell's fix is a ticker that marks the detached window's canvas dirty at
30fps for as long as an instance is detached. It is a workaround chosen because
it covers every applet rather than only the terminal; the precise version is
each view refreshing through the canvas it was handed.

### Canvas focus is not window focus

`Canvas.Focus` decides which widget receives keys **if the OS is sending keys
to that window**. `Window.RequestFocus` is what asks the window manager for the
window. Setting only the first gives a correctly focused widget that receives
nothing.

The signature is **first click does nothing, second click works** — the first
is spent by the window manager giving the window focus and never reaches the
application.

`RequestFocus` is a deliberate no-op under Wayland
(`internal/driver/glfw/window_desktop.go` opens with `if build.IsWayland {
return }`), where the compositor decides. There the first click remains the
fallback, so do not read a Wayland session's behaviour as a bug in your code.

### Focus can succeed without focusing

`FocusManager.Focus`, in `internal/app/focus_manager.go`:

```go
if !found { return false }
if hidden { return true }        // <- returns true WITHOUT focusing
```

If the object or any ancestor is not visible, it reports success and does
nothing. `Canvas.Focus` treats that as done and logs nothing. Just after
`Window.Show` the window has not been laid out, so a focus call there routinely
no-ops.

**`Canvas.Focused()` is the only honest signal that focus landed.** Call
`Focus`, then check `Focused()`, and retry if they disagree.

### Focus resolves by object identity

`FocusManager.Focus` walks the tree looking for `object == obj`. A wrapper that
embeds another widget — `*Session` embeds `*NativeTerminalWidget` — puts the
**outer** object in the tree while most of the code holds the inner one.
Passing the inner widget matches nothing, `Focus` returns false, and nothing is
focused, silently.

`SetFocusHost` records the outer object and `GrabFocus` is the only place that
calls `Canvas.Focus`, so the rule is applied once instead of at every call
site.

### Removing a tab does not release its content

`baseTabsRenderer.layout` shows and hides content by walking the items **still
in the list**, so a removed item's content is never touched again. Worse, when
removal empties the list, `setItems` calls `selectIndex(t, -1)`, which returns
before `Refresh` — there is no content pass at all.

The removed object can therefore stay parented in the old window's tree while
it is also being displayed in the new one, and a `CanvasObject` in two
containers lays out in neither.

Blank the tab's content to an empty label and refresh **before** removing the
tab. Then whatever is left behind is that label.

### A window is not its final size when `Show` returns

`glCanvas.SetContent` lays new content out at its **minimum** first:

```go
content.Resize(content.MinSize())
```

and the real geometry arrives a driver pass or two later. For most content that
is invisible. For a terminal it is not: the renderer's minimum is 400x300,
about 38 columns, and if the resize debounce is shorter than the gap the far
end is told 38 columns and a full-screen application redraws itself into a
corner.

Two defences, and both are worth having. Size the window before `SetContent`
and resize the content to the target immediately after, so a debounce restarts
on the right numbers. Then re-apply the true size once the window has settled.

### The settle loop

Neither of the two things worth waiting for reports itself, so poll for both at
once:

```go
func (i *Instance) step(attempt int, lastSize fyne.Size) {
    c := i.canvas()

    focused := c.Focused() == i.mount.Focus
    if !focused {
        c.Focus(i.mount.Focus)
        focused = c.Focused() == i.mount.Focus
    }

    size := c.Size()
    sized := size.Width > 0 && size == lastSize   // two equal readings

    if (focused && sized) || attempt >= settleAttempts {
        i.mount.OnPlaced()
        return
    }
    time.AfterFunc(settleInterval, func() {
        fyne.Do(func() { i.step(attempt+1, size) })
    })
}
```

Twenty attempts at 50ms covers a slow-mapping window. On exhaustion it fires
`OnPlaced` anyway — a terminal at the wrong column count is worse than one that
is merely unfocused, because a click fixes focus.

---

## Global settings and several instances

`ui.Settings` is process-wide. With one session on screen, "install the
session's settings, then build the widget" is enough, and that is what the
harnesses do. With several alive at once there is no single current value to
read: the last thing constructed decides for everything, including widgets
built before it existed.

Three kinds of state needed moving, and the distinction is worth internalising
before adding a fourth:

**Read at construction, per instance.** Font size, scrollback, paste pacing.
Install the settings, construct, restore the base immediately. If any of these
is read *live* instead — the paste delay was — it silently follows whatever was
mounted most recently.

**Baked into an object, per instance.** `widget.TextGrid` has no per-grid font
size; it always renders at `theme.SizeNameText`. The only way to give one
terminal its own size is `container.NewThemeOverride` with a theme that reports
that size, and the override object *holds* it. So the wrapper takes an explicit
settings snapshot rather than reading the global:

```go
func ThemedAt(obj fyne.CanvasObject, cfg Settings) fyne.CanvasObject {
    size := float32(ClampTerminalFontSize(cfg.FontSize))
    return container.NewThemeOverride(obj,
        NewTerminalFontTheme(NewNativeTheme(cfg.AppVariant()), size))
}
```

**Genuinely application-wide.** The chrome variant, the log directory, the grid
row/column offsets. These stay global and should not be per-instance; deriving
chrome per session would repaint the window on every tab change.

A per-instance value that falls back to a global when unset is a trap: it looks
correct until a second instance moves the global. Pin it explicitly at mount.

---

## Tables

`widget.Table` with `ShowHeaderRow`, `CreateHeader` and `UpdateHeader` is the
right primitive for anything list-shaped. Notes from the crawl view:

- Headers as `widget.Button` gives sortable columns for free — set
  `b.OnTapped` in `UpdateHeader` and mark the active column in its text.
- `SetColumnWidth` per column, once, at build time.
- `Label.Truncation = fyne.TextTruncateEllipsis` on cell labels. Without it a
  long value pushes the layout around.
- Reset any style you set conditionally. `l.TextStyle = fyne.TextStyle{}` at
  the top of `UpdateCell`, because cells are reused and a bold row will
  otherwise smear onto whatever scrolls into its place.
- **Column index and column list have to be edited together.** They are two
  places describing one thing, and inserting a column without renumbering the
  `switch` silently shifts every value one column right.
- Sorting must break ties deterministically **in both directions**, or
  reversing the sort scatters rows that share a key.

### Styling carries meaning

State is styled, and the styling has to agree with what the state means.
"Not dialed" is italic, not red: it is neither a success nor a failure, and if
it reads as a failure the distinction the counters exist to draw is undone by
the styling. A `2 !` in the attempts column is bold because it means a real
failed login happened.

The corollary is that the **majority** state gets no styling at all. On a
healthy capture run most rows are "unchanged", and a screen where most rows are
marked is a screen with no marks.

---

## Layout

- `container.NewBorder` for a view with a bar top or bottom.
- `container.NewVSplit` with an explicit `Offset` for primary-plus-detail.
- `container.NewAppTabs` with `NewTabItemWithIcon` for peer views over the same
  data — Decisions and Since-last-run in the crawl window.

Views expose `Content() fyne.CanvasObject` rather than a concrete container, so
the host can place them in a tab, a split, or a window without knowing what
they are made of.

**A composite cell in a form row is the first thing to suspect when a dialog
will not fit its window.** A form layout takes its minimum width from its
widest row; a two-column grid of two full-width widgets asks for twice one
field's minimum, and the excess propagates out through the tab to the button
bar, clipping buttons off the right edge. The session dialog's serial-port row
did exactly this. Fix: split it into two rows, each one widget.

---

## Views own no state

A view renders a model and holds only what is needed to render it — sort key,
filter, the snapshot it last drew. Everything else lives in a package with no
toolkit import, so it is testable without a display.

`crawlview.go` has no tests, and that is deliberate rather than lazy: the
honest test drives a display. `internal/crawlrun` underneath it is fully
covered, which is why the split exists at all. If logic is creeping into a
view, that is the signal it belongs one layer down.

The corollary: preview data belongs in the model package too. `crawlrun.Demo`
plays a scripted run with no vault, network, or devices, exercising every state
the table can show. It is data, it is tested, and keeping it out of the UI means
the widget layer is the only unverified thing in the preview path.

```
go run ./cmd/crawlui -demo
```

That is the loop for fixing layout and API mistakes — instant, and a layout bug
is visible in the first second rather than five minutes into a crawl.

**Demo mode has its own trap.** A hand-written script can emit a state the real
engine never produces, and then the preview looks perfect while the live run is
broken. The guard is a test that drives the real engine against `fakedev`,
collects the event kinds it actually emits, and asserts the demo script is a
**subset** of them. That requires the script to be readable as data —
`DemoEvents() []Event` separate from `Demo(run, opts)` playback.

---

## Callbacks out, not imports in

A view names what it wants done and lets the host decide how:

```go
view.OnInspect = func(d crawlrun.DeviceRow) { shell.ShowTranscript(d.Identity) }
view.OnConnect = func(d crawlrun.DeviceRow) { shell.OpenSession(d.Display()) }
```

This is what lets a crawl result open a session without the crawl view knowing
the terminal exists.

The shell applies the same rule to itself. It declares the interface it can
host —

```go
type Applet interface {
    Content() fyne.CanvasObject
    Start()
    Stop()
}
```

— and the three views satisfy it structurally, without importing the file that
declares it. The interface is declared at the **consumer**. That is the Go
translation of the base-class-with-virtual-hooks instinct every other desktop
toolkit trains: instead of views inheriting a contract, the host states the
shape it can hold and anything that fits, fits.

`internal/ui` imports no dialer, no vault, no crawler. Launching is a callback
the host installs. That constraint is the guard against the previous
generation's session manager, which reached two thousand lines by becoming the
place connections and dialogs both ended up.

---

## What can be tested, and how

`internal/ui` sits at about 5% coverage and that is the honest ceiling for
widget code. Everything below is about making that number not matter.

### Keep the model toolkit-free

`crawlrun`, `capturerun` and `shellmodel` import no toolkit, so they have real
tests, run under `-race`, and can be mutation-verified. The rule that keeps
this true: if a view is doing arithmetic, sorting, or deciding what a state
means, that belongs one layer down.

`shellmodel.go` is the smallest example — the shell's registry, instance
identity, unique-title allocation and counts, in a file importing `fmt` and
`sync`. Eight tests, race-clean.

### The scratch-package trick

When a file is *nearly* toolkit-free, copy it and its dependencies into a
throwaway module and compile it there. The theme layer was reduced to the point
where `settings.go`, `theme_registry.go`, `colors.go` and `paths.go` build
without Fyne, so all eight theme tests actually ran — in an environment where
`internal/ui` as a whole could not build at all.

This is also how to verify a change on a machine that cannot build Fyne.

### Static checks that catch what the compiler would

Three AST scans have each caught something real:

- **Duplicate declarations across the package.** `internal/ui` is thirty files
  in one package; a helper named `labelled` added twice compiles nowhere and
  the error names only one of them.
- **Unused imports.**
- **Symbol cross-check.** Resolve every `pkg.Symbol` reference in a file
  against the declarations that actually exist in that package. This is what
  carried `shell.go`, `launchforms.go` and `cmd/pathfinder/main.go` to a clean
  first build without ever compiling them, and it caught
  `capture.NewFileStore`, which is really `OpenFileStore`.

None of these replaces a build. They make a build on someone else's machine
likely to pass on the first try, which is the actual constraint when the person
who writes the code cannot run the toolkit.

### Mutation-verify the guard

A test that passes proves nothing about whether it would fail. Break the thing
it guards and confirm it complains:

- Remove the `RWMutex` from the registry → `WARNING: DATA RACE`, test fails.
- Re-introduce the parsed-template cache the comment warns against → seven data
  races.
- Make `ThemeDef.IsDark()` read the app variant → two theme tests fail.

**The guard's own helper is what usually goes wrong.** The first version of the
demo-superset guard under-drove the engine — no bindings, so one event kind was
unreachable — and blamed a correct demo script. When a new guard fails, suspect
its setup before suspecting the code.

### Where `-race` earns its keep

Every emit path is a worker goroutine writing to something a redraw loop reads.
Run the model packages under `-race` routinely; it found an unlocked map in a
test recorder that passed reliably without it. The race detector is
probabilistic, so `-count=5` is the insurance if a known-racy path ever goes
quiet.

---

## Races and silent failures

Beyond the toolkit's own, three patterns have each bitten more than once.

### Widget state and cached state disagree

A `widget.List`'s selection is cleared by `Refresh`, and code that caches "what
is selected" does not hear about it. The two then disagree and the stale one
wins:

```go
// Wrong. After a Refresh the widget has no selection but selDevice still
// holds the last one, so clicking that device again does nothing at all.
if v.selDevice == canonical { return }
```

Three instances of this shape in the capture store browser. The fix in each was
to drop the cached-state guard and, where the code decides something is
selected, tell the widget too — `typeList.Select(at)` — so the two cannot
diverge.

### A silent `return` in a click handler is a bug

```go
if v.store == nil || canonical == "" || typ == "" { return }
```

The user clicks, nothing appears, and there is no way to tell that from a
broken read. Every guard in a handler either acts or reports. This is the same
principle as the toolkit problems above, applied to our own code.

### Announce nothing you have not verified

`Canvas.Focus` claiming success is the toolkit's version. Ours was a status
line that said what a run *would* do. If a call has a way to be checked, check
it before reporting.

---

## Reading the toolkit

Two rounds of careful reasoning about the detach bug produced two wrong fixes.
Downloading Fyne's source settled it in ten minutes.

```bash
curl -sL https://codeload.github.com/fyne-io/fyne/tar.gz/refs/tags/v2.6.2 \
  | tar xz --strip-components=1 -C /tmp/fynesrc
```

The files worth knowing:

    internal/cache/canvases.go            the object->canvas cache
    canvas/canvas.go                      canvas.Refresh -> that cache
    internal/app/focus_manager.go         Focus, and when it lies
    internal/driver/common/canvas.go      Canvas.Focus / Refresh / SetContent
    internal/driver/glfw/window_desktop.go  RequestFocus, Wayland
    container/tabs.go                     show/hide of tab content
    container/apptabs.go                  AppTabs specifics

**When a toolkit behaves inexplicably twice, stop inferring and read it.** The
cost is minutes; the cost of a third wrong fix is a whole session and a tester's
patience.

---

## Building

`fyne.io` is not always reachable from restricted environments, so the UI
packages are the ones that fail first when the module cache is cold. Nothing
about that is fixable here; it is worth knowing when a build fails on
`cmd/crawlui` and nothing else.

`go run cmd/crawlui` fails with a message about `cmd/` not being in std — that
path is read as a standard-library package. It needs the `./`:

```
go run ./cmd/crawlui
```

`test.sh` keeps a `FLOOR_EXCLUDES` list of the Fyne-importing directories so
the rest of the tree can be verified in an environment that cannot build them.
Every new `cmd/` front end has to be added to it.

---

## Open

**No transcript view yet.** `OnInspect` fires and prints. Crawl output is
captured command output, not an interactive session, so this wants a pager and
not a PTY — the terminal widget can render it read-only.

**The detached repaint ticker is a workaround.** It redraws a detached window
thirty times a second whether or not anything changed. The precise fix is for
each view to refresh through the canvas the shell hands it in
`OnCanvasChange`, the same way the terminal resolves focus.

**`OnCanvasChange` and `OnPlaced` are wired only for terminals.** Crawl and
capture ignore both. Nothing is broken today because neither resolves a canvas
itself — but the first context menu or dialog added to either view will hit the
same cache.

**The redraw loop is written three times.** Coalesce flag, ticker, `fyne.Do`,
in `crawlview.go`, `captureview.go` and the terminal's update processor. It is
the one thing in `internal/ui` genuinely worth extracting as an embeddable
type.

**Detached geometry is not remembered**, and there is no drag-out-to-detach.