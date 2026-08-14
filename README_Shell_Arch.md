# Shell Architecture

How PathfinderSSH puts a terminal, a crawl and a capture in one window.

    internal/ui/shell.go        tabs, instances, the move between window and tab
    internal/ui/shellmodel.go   the registry — no toolkit
    internal/ui/launchforms.go  the two parameter dialogs
    cmd/pathfinder              the host: the only thing here that connects

`shellmodel.go` imports `fmt` and `sync`, nothing else, which is why the
bookkeeping has real tests. `shell.go` imports Fyne and no dialer, no vault, no
crawler. `cmd/pathfinder` imports all of those and owns every connection.

The harnesses stay: `cmd/pfterm`, `cmd/pfconnect`, `cmd/crawlui` and
`cmd/captureui` are still the fastest way to reproduce a bug in one view with
the shell out of the way, and they mean the shell can be broken without
breaking the four things that already work.

---

## The problem this solves

Three views existed that had never met. Each had its own state, its own
rendering, its own idea of when to redraw. The question was what kind of object
holds them.

Every desktop toolkit that shaped the instinct here — Qt, JavaFX, WinForms — is
object-oriented by design, and the reflex is a base class with virtual hooks
that each view overrides. That reflex is wrong in Go, and following it would
have produced the thing this project exists to avoid: TetherSSH's session
manager reached two thousand lines by becoming the place where connections,
dialogs and tab management all ended up.

The shell hosts. It does not connect, it does not ask the user anything, and it
does not know that a terminal exists.

---

## What an applet is

Nothing. There is no base type and no embedding.

    type Applet interface {
        Content() fyne.CanvasObject
        Start()
        Stop()
    }

Three views arrived at those three methods independently, before there was a
shell to satisfy. `CrawlView` and `CaptureView` implement this interface
without a single line of change and without importing the file that declares
it. A terminal needs a four-line adapter in the host, because a `ui.Session` is
already a `CanvasObject` and has no redraw loop of its own to gate.

The interface is declared at the consumer, not the producer. That is the whole
of the translation from the OO version: instead of views inheriting a contract,
the host states the shape it can hold and anything that fits, fits. Adding a
fourth applet means writing three methods, not joining a hierarchy.

`Start` releases a view's redraw loop and `Stop` ends it. **`Stop` is teardown,
not pause**, so hiding a tab does not call it. A backgrounded terminal must
keep reading its socket and a backgrounded crawl must keep aggregating;
switching tabs would otherwise quietly cost data.

---

## Placement is data

`Content()` returns a `fyne.CanvasObject` and the view never learns where it
lives, so a tab and a window are the same thing to it. Moving between them is a
re-parent, not a rebuild, and no state migrates because no state moved.

An `Instance` holds the applet, its `root` (the instance bar plus the applet's
content, built once), and exactly one of a `*container.TabItem` or a
`fyne.Window`. Detaching swaps which of those two is nil. `root` is built once
on purpose: rebuilding it per move would discard the widget state the move
exists to preserve.

The one hard rule is that a `CanvasObject` can be in exactly one container.
Every move here removes before it adds — including from the window, which is
handed a blank label before its content is passed to a tab.

### The instance bar

Each instance carries a one-row bar: the caller's actions on the left, a status
label in the middle, detach/redock and close on the right. The controls belong
to the instance rather than to the window, so a detached crawl takes its Stop
button with it.

Fyne's `AppTabs` has no closable tabs and no per-tab decoration hook, which is
why this is a bar inside the content rather than something on the tab itself.

---

## The move, and what Fyne does not tell you

This is the part that took five attempts, and every one of them failed
silently. It is written up at length because the failure mode generalises: **a
widget that can move between windows invalidates every cached window or canvas
reference in the codebase.**

### The write-once canvas cache

`fyne.Driver.CanvasForObject` reads a map written by `cache.SetCanvasForObject`,
and that writer uses `LoadOrStore`. The entry records the *first* canvas an
object rendered on and is never updated. Rendering the same object on a second
canvas keeps the original. Entries expire only when they stop being touched,
and every lookup marks them alive, so an on-screen object's entry never
expires. There is no API to correct it.

Two separate paths run through that lookup, and both break for a moved widget.

**Focus.** The terminal's `MouseDown` calls `GrabFocus`, which resolved its
canvas from the driver — so the first click inside a detached session handed
focus back to the window the session used to be in. The same lookup made
`ShowContextMenuAt` open its popup on the main window, behind the one the
pointer was in.

The fix is an explicit override. `NativeTerminalWidget.SetHostCanvas` records
where the widget actually is; `FocusCanvas` prefers it and falls back to the
driver, and `GrabFocus` and `HostWindow` both route through `FocusCanvas`. The
shell calls it via `Mount.OnCanvasChange` on every mount and every move, which
keeps the shell ignorant of terminals — it hands over a canvas, and whatever
cares does something with it.

**Repaint.** `canvas.Refresh(obj)` resolves through the same lookup, so a
detached applet marks its *original* canvas dirty on every redraw and its own
canvas never. The window then repaints only when something else happens to
dirty it. The symptom was a selection dragged in a detached terminal staying
invisible until a popup menu was opened, at which point it appeared all at
once — the popup's objects were new, they registered on the right canvas, and
the whole window caught up.

`Instance.startRepaint` marks the detached window's canvas dirty on a 33ms
ticker, matching the terminal's own update processor. It lives in the shell
rather than the terminal because crawl and capture have the identical latent
bug. See *Known gaps* for what that costs.

### Canvas focus is not window focus

`Canvas.Focus` decides which widget receives keys **if the OS is sending keys
to that window**. `Window.RequestFocus` is what asks the window manager for the
window. Setting only the first gives a correctly focused terminal that receives
nothing, and the user's first click is spent by the window manager rather than
reaching the application — the signature is *first click does nothing, second
click works*.

Both are set. `RequestFocus` is a documented no-op under Wayland, where the
compositor decides; there the first click remains the fallback.

### Focus can succeed without focusing

`FocusManager.Focus` returns `true` without focusing when the object or any
ancestor is not yet visible, and `Canvas.Focus` treats that as success and logs
nothing. Just after `Window.Show` the window has not been laid out, so a focus
call there routinely no-ops.

`Canvas.Focused()` is the only honest signal that focus landed, which is why
`settle` polls rather than calling once and trusting it.

### Removing a tab does not release its content

`AppTabs` shows and hides content by walking the items still in its list, so a
removed item's content is never touched again — and when removal empties the
list, `setItems` calls `selectIndex(t, -1)`, which returns before `Refresh`, so
there is no content pass at all. The removed object can stay parented in the
main window's tree while it is also being displayed elsewhere.

`Shell.releaseTab` blanks a tab's content to an empty label and refreshes
*before* removing the tab, so anything left behind is that label.

---

## Settling

A window is not its final size when `Show` returns. `Canvas.SetContent` lays
new content out at its **minimum** first, and the real geometry arrives a
driver pass or two later.

For a terminal that is not cosmetic. The renderer's minimum is 400x300, which
is about 38 columns; `handleResize` debounces by 150ms, and the true size
arrived later than that, so the far end was told 38 columns and a full-screen
application redrew itself into a corner.

`settle` is one poll loop — twenty attempts at 50ms — waiting on two conditions
that neither report themselves: `Canvas.Focused()` matching the applet's
focusable, and two consecutive equal readings of `Canvas.Size()`. When both
hold it fires `Mount.OnPlaced`, which the host wires to
`NativeTerminalWidget.ResyncSize`: recompute from the current size, cancel any
pending debounce, apply immediately.

On exhaustion it fires `OnPlaced` anyway. A terminal at the wrong column count
is worse than one that is merely unfocused, because a click fixes focus.

The debounce is also pre-empted: `Detach` sizes the window before `SetContent`
and resizes `root` to the target immediately after, so the debounce restarts on
the right numbers and the minimum never escapes to the device. `OnPlaced` is
the belt to that braces.

---

## Per-instance state that used to be global

`ui.Settings` is process-wide. With one session on screen that is fine and
"install the session's settings, then build" is enough. With several alive at
once there is no single current value to read — the last tab opened would
decide for every tab, including ones built before it existed.

Three things moved:

**`ThemedAt(obj, cfg)`** takes an explicit settings snapshot; `Themed`
delegates to it with the current global. The returned override object *holds*
the font size, so a terminal keeps its own when a later mount installs
different settings.

**`NativeTerminalWidget.pasteLineDelayMs`** is read at construction, beside
font size and scrollback. It was the only genuinely *live* read of a global
that is a per-session setting. Row and column offsets and the log directory are
application-level and correctly stay global.

**The terminal palette is pinned explicitly** rather than left to fall through
to the global, which the next mount would move.

The mount order in the host is therefore: install this session's settings,
construct, `ApplySession`, pin the palette, wrap with `ThemedAt`, restore the
base settings *immediately*, then `Attach`. `ApplySession` must precede
`Attach` because anti-idle is read when the transport is attached.

---

## Launching

`Mount` carries no dialer and the shell has no launch logic. `Shell.AddLauncher`
takes a label and a function; `cmd/pathfinder` supplies three.

The dialogs are data in, data out, the same rule `SessionForm` already
followed. `ShowCrawlDialog` and `ShowCaptureDialog` return `CrawlLaunch` and
`CaptureLaunch` — a `Params` plus a local `LaunchAuth`, which exists so that
`internal/ui` does not import `crawldial` or `capturedial`. Known capture types
are passed *in* by the host for the same reason.

Both dialogs open on the values last used in the process. A dialog that opens
empty every time is slower than the command line it replaces, and the person
using it goes back to the command line. Persisting across runs is
`crawlrun.Profiles`' job and is not wired.

A Fyne confirm dialog cannot refuse to dismiss, so a validation failure
re-shows the **same content object**. Nothing typed is lost because the widgets
were never rebuilt.

The vault is unlocked once, at startup, before any window exists — it prompts
on the controlling terminal, and doing that later means prompting somewhere
nobody is looking. The dialogs get a credential picker; they never get a
password prompt.

---

## The loop this exists to close

`CrawlView.OnConnect` was a `fmt.Printf` in `cmd/crawlui` for as long as there
was nowhere to send it. In the shell it opens a session dialog prefilled with
the device, which connects into a new tab beside the crawl that found it.

That is the first time in this project that a device discovered by the crawler
became a session without anyone retyping its name, and it is the reason the
shell was worth building rather than four separate windows.

---

## Threading

`Open`, `Close`, `Detach` and `Redock` all touch Fyne containers and must run
on the UI goroutine. A dial or a crawl that finishes elsewhere hops with
`fyne.Do` first.

Reading the registry is safe from anywhere — it is `RWMutex`-guarded so that a
transport closing on its own goroutine can ask what is still open without
hopping threads. Writes stay on the UI goroutine because every one of them is
paired with a container change.

`Close` is guarded by a `CompareAndSwap`: the close button, the window's close
box, and a transport that died can all reach it. It stops the applet first, then
unparents, then runs `Mount.OnClose` **on its own goroutine** — closing a serial
port whose adapter was unplugged blocks in the driver, and doing that inline
freezes the window instead of closing the tab.

A detached window's close box **re-docks** rather than destroying the instance.
Losing a live session to a stray click is the kind of surprise that stops
people using a feature; the close button in the instance bar is how you
actually end one.

---

## Known gaps

**The repaint ticker is a workaround.** It redraws a detached window thirty
times a second whether or not anything changed. For a terminal that is what
would be happening anyway; for an idle detached crawl it is waste. The precise
fix is for each view to refresh through the canvas the shell hands it in
`OnCanvasChange`, the same way the terminal now resolves focus. Worth doing the
first time a detached window's CPU shows up.

**`OnCanvasChange` and `OnPlaced` are wired only for terminals.** Crawl and
capture ignore both. They do not currently resolve a canvas themselves, so
nothing is broken — but a right-click menu or a dialog added to either view
will hit the same cache, and the hook is already there.

**Redraw loops are not gated on visibility.** Three coalesced 200ms tickers run
whether or not their tab is in front. The correct split, if it ever costs
anything, is that a model's work loop ignores visibility and a view's redraw
loop obeys it.

**The redraw loop is written three times.** Coalesce flag, ticker, `fyne.Do`,
in `crawlview.go`, `captureview.go` and the terminal's update processor. It is
the one thing in `internal/ui` genuinely worth extracting as an embeddable
type.

**Detached geometry is not remembered.** Every detach opens at 1100x720 and a
redock-then-detach loses whatever size and position the user chose.

**There is no drag-out-to-detach.** Detaching is a button in the instance bar,
which is discoverable but not what anyone's hands expect.

**The instance status line duplicates the applet's own summary.** For a
terminal it earns its place — target plus connection state. For a crawl it
echoes parameters the view already summarises better, and the two can read as
contradicting each other: the bar says the requested depth while the view says
the deepest reached so far. It should probably carry only errors and final
counts for those two.

**There is no session tree.** The shell launches from dialogs; the YAML
inventory, its merge-on-import, and the tree widget are still ahead.

**Nothing routes between applets except crawl to terminal.** A capture row does
not open a session, a terminal does not seed a crawl, and the map surface does
not exist yet.