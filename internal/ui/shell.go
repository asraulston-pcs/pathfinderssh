// internal/ui/shell.go
//
// The application shell: a toolbar that launches applets and a tab strip that
// holds them, with any instance able to move out into a window of its own.
//
// # What an applet is
//
// Nothing. There is no base type and no embedding. Three views arrived at the
// same three methods independently -- Content, Start, Stop -- so the shell
// declares that shape as an interface and they satisfy it without knowing this
// file exists. Adding a fourth applet means writing those three methods, not
// inheriting anything.
//
// # Placement is data
//
// Content() returns a fyne.CanvasObject and the view never learns where it
// lives, so a tab and a window are the same thing to it: moving between them is
// a re-parent, not a rebuild, and no state is migrated because no state moved.
// The one hard rule is that a CanvasObject can be in exactly one container --
// every move here removes before it adds.
//
// # What the shell must not do
//
// It does not connect. Nothing in this file imports a dialer, a vault, or a
// crawler; launching is a callback the host installs. That is the guard against
// what happened to TetherSSH's session manager, which grew to 2,000 lines by
// becoming the place connections and dialogs both ended up.
//
// # Threading
//
// Open, Close, Detach and Redock all touch Fyne containers, so they must run on
// the UI goroutine. A dial or a crawl that finishes elsewhere hops with
// fyne.Do first. Reading the registry is safe from anywhere.
package ui

import (
	"fmt"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Applet is what the shell can host. CrawlView and CaptureView already satisfy
// it; a terminal is wrapped by a few lines in the host.
//
// Start releases the view's redraw loop and Stop ends it. Both are idempotent
// in the existing views, and Stop is permanent: it is a teardown, not a pause.
// Hiding a tab therefore does NOT call Stop -- a backgrounded terminal must
// keep reading its socket and a backgrounded crawl must keep aggregating, or
// switching tabs would quietly cost data.
type Applet interface {
	Content() fyne.CanvasObject
	Start()
	Stop()
}

// Mount is everything the shell needs to host one instance.
type Mount struct {
	Kind Kind

	// Title is the tab label, made unique if it collides.
	Title string

	// Applet is the thing being hosted. Its Content must already be
	// wrapped in whatever the applet needs -- for a terminal that means
	// ThemedAt, which the shell cannot do for it because the shell does not
	// know the session's settings.
	Applet Applet

	// Actions are placed at the left of the instance bar: a Stop button for
	// a crawl, a reconnect for a terminal. They belong to the instance
	// rather than to the window, so a detached instance takes its own
	// controls with it.
	Actions []fyne.CanvasObject

	// Focus is given keyboard focus when the instance's tab is selected.
	// A terminal that does not take focus back on tab change looks broken
	// in a way that is easy to blame on the terminal.
	Focus fyne.Focusable

	// OnCanvasChange is called with the canvas the instance now lives on,
	// every time it is mounted or moved between the tab strip and a window
	// of its own.
	//
	// This is not a convenience. Fyne's object->canvas cache is written
	// with LoadOrStore, so it records the FIRST canvas an object was
	// rendered on and never updates. Any applet that resolves a canvas or
	// a window through the driver -- for focus, popup menus, dialogs --
	// gets the wrong one forever after a move, with no error and with the
	// widget still rendering correctly in its new home. An applet that
	// never asks the driver where it is does not need this.
	OnCanvasChange func(fyne.Canvas)

	// OnPlaced is called once per mount or move, after the window hosting
	// the instance has reached its real size.
	//
	// A window is not its final size when Show returns -- Canvas.SetContent
	// lays content out at its MINIMUM first, and the true geometry arrives
	// a driver pass or two later. Anything that derives state from its own
	// dimensions, a terminal's row and column count above all, has to be
	// told when that has finished or it acts on the minimum.
	OnPlaced func()

	// OnClose runs after the applet is stopped and the instance is gone
	// from the UI. It is called on its own goroutine, because closing a
	// serial port whose adapter was unplugged can block in the driver and
	// doing that inline freezes the window instead of closing the tab.
	OnClose func()

	// Busy reports what this instance would lose if it were closed right
	// now -- "connected", "running" -- or "" when it would lose nothing.
	//
	// The shell cannot answer this itself, which is the point: a tab is
	// open either way, and only the host holds the session or the cancel
	// func that knows the difference between a live crawl and a table of
	// last hour's results. An applet with no Busy is never counted, which
	// is the right default: silence means nothing to lose.
	//
	// It is asked on the UI goroutine while the person waits, so it must
	// answer from state it already has. No dialing, no file reads, no
	// blocking.
	Busy func() string
}

// Instance is one hosted applet. The shell owns it; the host holds the pointer
// to set status or close it programmatically.
type Instance struct {
	// reclaiming collapses a burst of layout passes into one focus check.
	reclaiming atomic.Bool

	shell *Shell
	info  *InstanceInfo
	mount Mount

	// root is bar + content, built once. It is what moves between the tab
	// and the window, which is why it is built once: rebuilding it on every
	// move would discard the applet's widget state.
	root   fyne.CanvasObject
	status *widget.Label
	place  *widget.Button

	tab *container.TabItem
	win fyne.Window

	// repaint drives the detached window's redraws. See startRepaint.
	repaint chan struct{}

	closed atomic.Bool
}

// ID is the instance's stable identifier.
func (i *Instance) ID() int { return i.info.ID }

// Title is the unique display title.
func (i *Instance) Title() string { return i.info.Title }

// SetStatus writes the instance's status line. Safe only on the UI goroutine.
func (i *Instance) SetStatus(msg string) { i.status.SetText(msg) }

// SetTitle renames the instance and whatever is currently displaying it.
//
// An instance is either a tab or a window and never both, so exactly one of
// the two branches applies — the same split Detach and Redock maintain. A
// rename that only updated the registry would leave the strip showing the
// previous name, which for a re-run search means a tab labelled with the
// query it used to hold.
//
// UI goroutine only, like the rest of the shell.
func (i *Instance) SetTitle(base string) {
	title, ok := i.shell.reg.Rename(i.info.ID, base)
	if !ok {
		return
	}
	if i.tab != nil {
		i.tab.Text = title
		i.shell.tabs.Refresh()
	}
	if i.win != nil {
		i.win.SetTitle(title)
	}
}

// Shell is the application window's content: a launcher toolbar over a tab
// strip.
type Shell struct {
	app  fyne.App
	win  fyne.Window
	tabs *container.DocTabs
	reg  *Registry

	byID    map[int]*Instance
	summary *widget.Label

	// focusWatch stops the keyboard-focus watchdog. See StartFocusWatch.
	focusWatch chan struct{}
	bar        *fyne.Container
	content    *fyne.Container
	split      *container.Split
	body       fyne.CanvasObject
}

// NewShell builds the shell. Call it after app.New(): it constructs widgets,
// and a widget built before the app exists nil-derefs inside
// Button.CreateRenderer with a panic that names a layout function.
func NewShell(a fyne.App, w fyne.Window) *Shell {
	s := &Shell{
		app:  a,
		win:  w,
		reg:  NewRegistry(),
		byID: map[int]*Instance{},
	}

	// DocTabs rather than AppTabs: it draws a close control on the tab
	// itself and collapses the strip into an overflow menu once there are
	// more tabs than fit, both of which AppTabs lacks. Ten open sessions is
	// a normal day, and the only way to end one used to be a small icon in
	// the instance bar.
	s.tabs = container.NewDocTabs()
	s.tabs.SetTabLocation(container.TabLocationTop)
	s.tabs.OnSelected = func(t *container.TabItem) { s.focusTab(t) }

	// CloseIntercept, not OnClosed. Without an intercept DocTabs removes the
	// item itself and only then tells anyone, so the applet would still be
	// running, the transport still open and the registry still counting it --
	// a tab that vanished and a session that did not. With one, the X is just
	// another way into Instance.Close, which is the single teardown path the
	// close button and the window close box already use.
	s.tabs.CloseIntercept = func(t *container.TabItem) {
		if inst := s.instanceFor(t); inst != nil {
			inst.Close()
		}
	}

	s.summary = widget.NewLabel("Nothing open — use Launch")
	s.bar = container.NewHBox()
	top := container.NewBorder(nil, nil, s.bar, nil, s.summary)

	s.body = s.tabs
	s.content = container.NewBorder(top, nil, nil, nil, s.body)
	return s
}

// SetSide docks an object down the left of the window, beside the tabs.
//
// The shell does not know or care what it is — an inventory tree today, a
// filter panel later. offset is the fraction of the width the side takes;
// values outside a usable range are clamped rather than honoured, because a
// split at 0.02 is a pane nobody can grab back.
//
// Calling it a second time replaces the side. Passing nil removes it and
// returns the tabs to full width, which is what a "hide the tree" toggle needs.
func (s *Shell) SetSide(obj fyne.CanvasObject, offset float64) {
	if obj == nil {
		s.split = nil
		s.body = s.tabs
	} else {
		if offset < 0.1 {
			offset = 0.1
		}
		if offset > 0.6 {
			offset = 0.6
		}
		s.split = container.NewHSplit(obj, s.tabs)
		s.split.SetOffset(offset)
		s.body = s.split
	}

	// container.NewBorder appends the middle object FIRST and then the edges,
	// so index 0 is the body and the toolbar the layout holds by reference is
	// untouched. Swapping one element beats rebuilding the container, which
	// would re-parent the toolbar and the summary label for no reason.
	s.content.Objects[0] = s.body
	s.content.Refresh()
}

// SideOffset is where the splitter currently sits, or 0 with no side panel.
// Worth persisting: a pane the person resized should still be that width next
// time the application starts.
func (s *Shell) SideOffset() float64 {
	if s.split == nil {
		return 0
	}
	return s.split.Offset
}

// Content is the object to put in the main window.
func (s *Shell) Content() fyne.CanvasObject { return s.content }

// Window exposes the shell's window for a caller that has to raise a dialog
// against it. It is deliberately not a way to reach the content.
func (s *Shell) Window() fyne.Window { return s.win }

// Registry exposes the bookkeeping for read-only use -- a status line, a
// "quit with sessions open?" prompt. Safe from any goroutine.
func (s *Shell) Registry() *Registry { return s.reg }

// AddLauncher puts a button in the toolbar. The host supplies the function,
// which is what keeps dialers and dialogs out of this file.
func (s *Shell) AddLauncher(label string, icon fyne.Resource, fn func()) {
	var b *widget.Button
	if icon != nil {
		b = widget.NewButtonWithIcon(label, icon, fn)
	} else {
		b = widget.NewButton(label, fn)
	}
	s.bar.Add(b)
	s.bar.Refresh()
}

// AddToolbar puts an arbitrary object in the toolbar, after the launchers.
//
// For status and state that is not a launch — a vault lock indicator, a
// connection count. The shell places it and never touches it again; the caller
// keeps the reference and updates it.
func (s *Shell) AddToolbar(obj fyne.CanvasObject) {
	s.bar.Add(obj)
	s.bar.Refresh()
}

// Open mounts an applet as a new tab and returns its instance.
//
// UI goroutine only. It calls Start() before the tab is added, which is safe
// here and would not have been at program startup: by the time the shell can
// open anything the driver is running, so the redraw loop has somewhere to hand
// work to.
func (s *Shell) Open(m Mount) *Instance {
	info := s.reg.Add(m.Kind, m.Title)

	inst := &Instance{
		shell:  s,
		info:   info,
		mount:  m,
		status: widget.NewLabel(""),
	}
	inst.status.Truncation = fyne.TextTruncateEllipsis

	inst.place = widget.NewButtonWithIcon("", theme.ViewFullScreenIcon(), inst.togglePlacement)
	closeBtn := widget.NewButtonWithIcon("", theme.CancelIcon(), inst.Close)
	closeBtn.Importance = widget.LowImportance
	inst.place.Importance = widget.LowImportance

	left := container.NewHBox(m.Actions...)
	right := container.NewHBox(inst.place, closeBtn)
	bar := container.NewBorder(nil, nil, left, right, inst.status)

	// The applet sits under a layout that reports when it has been resized.
	// That is the only signal available for the case below: dragging the
	// split divider destroys keyboard focus, and nothing in the toolkit says
	// so.
	body := container.New(&notifyLayout{onLayout: inst.queueReclaim}, m.Applet.Content())
	inst.root = container.NewBorder(bar, nil, nil, nil, body)

	// Start before the tab exists. The view's loop is gated on this and
	// coalesces its own redraws, so releasing it early costs one tick of
	// drawing into a container that is about to be shown.
	m.Applet.Start()

	inst.tab = container.NewTabItem(info.Title, inst.root)
	s.byID[info.ID] = inst
	s.tabs.Append(inst.tab)
	s.tabs.Select(inst.tab)
	s.refreshSummary()
	inst.settle()
	return inst
}

// releaseTab blanks a tab's content and then removes the tab.
//
// The blanking is the point. AppTabs shows and hides content by walking the
// items that are STILL in the list, so an item's content is never touched
// again once it has been removed -- and when the removed item was the last
// one, selection goes to -1 and the renderer does no content pass at all. The
// object can therefore stay parented in the main window's tree while it is
// also being displayed somewhere else, which is the one thing a CanvasObject
// may not do. Blanking first means anything left behind is an empty label.
func (s *Shell) releaseTab(item *container.TabItem) {
	if item == nil {
		return
	}
	item.Content = widget.NewLabel("")
	s.tabs.Refresh()
	s.tabs.Remove(item)
	s.tabs.Refresh()
}

// Close tears down an instance. Idempotent: the close button, the window's
// close box and a transport that died can all arrive at it.
func (i *Instance) Close() {
	if !i.closed.CompareAndSwap(false, true) {
		return
	}
	// Stop the redraw loop first. After this nothing schedules work onto
	// the UI goroutine for widgets that are about to be unparented.
	i.mount.Applet.Stop()
	i.stopRepaint()

	if i.tab != nil {
		i.shell.releaseTab(i.tab)
		i.tab = nil
	}
	if i.win != nil {
		w := i.win
		i.win = nil
		// Clear the intercept first: it re-docks, and re-docking a
		// closing instance would put a dead applet back in a tab.
		w.SetCloseIntercept(nil)
		w.Close()
	}

	i.shell.reg.Remove(i.info.ID)
	delete(i.shell.byID, i.info.ID)
	i.shell.refreshSummary()

	if i.mount.OnClose != nil {
		go i.mount.OnClose()
	}
}

// detachedWindowSize is what a freshly detached instance opens at. Large
// enough that a terminal is usable without being resized first.
var detachedWindowSize = fyne.NewSize(1100, 720)

// startRepaint keeps a detached window painting.
//
// This is a workaround for a toolkit fact, and it is worth stating exactly
// which one. Every repaint request in Fyne ends at
//
//	canvas.Refresh(obj) -> Driver.CanvasForObject(obj) -> c.Refresh(obj)
//
// and CanvasForObject reads the write-once cache: for an object that has been
// MOVED, it returns the window the object was first shown in. So a detached
// applet marks its ORIGINAL canvas dirty on every redraw and its own canvas
// never. The window then only repaints when something else happens to dirty
// it -- which is why a selection dragged in a detached terminal stayed
// invisible until a popup menu was opened, and then appeared all at once.
//
// Marking the right canvas dirty on a timer is the smallest fix that covers
// every applet rather than only the terminal. The cost is a redraw per tick
// for as long as an instance is detached, which for a terminal is what would
// be happening anyway; for an idle detached crawl it is waste, and if that
// ever matters the precise fix is for each view to refresh through the canvas
// the shell hands it in OnCanvasChange.
func (i *Instance) startRepaint() {
	i.stopRepaint()
	done := make(chan struct{})
	i.repaint = done

	go func() {
		t := time.NewTicker(repaintInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fyne.Do(func() {
					if i.closed.Load() || i.win == nil {
						return
					}
					if c := i.win.Canvas(); c != nil {
						c.Refresh(i.root)
					}
				})
			}
		}
	}()
}

func (i *Instance) stopRepaint() {
	if i.repaint != nil {
		close(i.repaint)
		i.repaint = nil
	}
}

// repaintInterval matches the terminal's own ~30fps update processor, so a
// detached session is no less responsive than a docked one.
const repaintInterval = 33 * time.Millisecond

// canvas is the canvas this instance is currently displayed on.
//
// Resolved from the shell's own bookkeeping rather than from
// fyne.Driver.CanvasForObject, which cannot answer this question for anything
// that has moved: its cache is written with LoadOrStore and keeps the first
// canvas an object appeared on.
// queueReclaim schedules one focus check for the next UI tick.
//
// It is called from a layout pass, which must not focus anything itself --
// focusing mid-layout re-enters the toolkit -- and which fires dozens of times
// during a single drag. The atomic collapses that burst into one check.
func (i *Instance) queueReclaim() {
	if i.closed.Load() || i.mount.Focus == nil {
		return
	}
	if !i.reclaiming.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(settleInterval, func() {
		fyne.Do(func() {
			i.reclaiming.Store(false)
			i.reclaimFocus()
		})
	})
}

// StartFocusWatch begins the keyboard-focus watchdog. Call it immediately
// before ShowAndRun, for the same reason an applet's Start is called there: the
// loop hands work to fyne.Do, which needs a running driver.
//
// # Why a poll and not an event
//
// Keyboard focus in this toolkit can be dropped by things that report nothing
// and belong to no applet. The press handler unfocuses the canvas whenever the
// object under the cursor has no focusable ancestor, so a split divider, a gap
// in a toolbar or an overlay being torn down can each leave the canvas with
// NOBODY focused -- and the only symptom is a terminal that has stopped taking
// keys. Instance.queueReclaim catches the cases that happen to cause a layout
// pass; this catches the rest.
//
// It is safe because reclaimFocus only ever fills a vacuum: if anything at all
// holds focus, including a dialog or a filter box the person deliberately
// clicked into, this does nothing. It can restore focus, never steal it.
//
// The cost is a map walk and a nil check per tick, so the interval is generous
// -- long enough not to be busy work, short enough that recovery reads as
// instant rather than as something the person had to fix.
func (s *Shell) StartFocusWatch() {
	s.StopFocusWatch()
	done := make(chan struct{})
	s.focusWatch = done

	go func() {
		t := time.NewTicker(focusWatchInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fyne.Do(func() {
					for _, inst := range s.byID {
						inst.reclaimFocus()
					}
				})
			}
		}
	}()
}

// StopFocusWatch ends the watchdog. Idempotent.
func (s *Shell) StopFocusWatch() {
	if s.focusWatch != nil {
		close(s.focusWatch)
		s.focusWatch = nil
	}
}

// focusWatchInterval is deliberately slower than the repaint ticker: this loop
// is a safety net rather than a rendering path, and a quarter second is below
// the threshold at which a person reaches for the mouse.
const focusWatchInterval = 250 * time.Millisecond

// reclaimFocus takes keyboard focus back, but ONLY when nothing at all holds
// it.
//
// The case this exists for is in the toolkit, not in this application:
// glfw's press handler unfocuses the canvas whenever the object under the
// cursor is not fyne.Focusable and has no focusable ancestor. A split divider
// is not focusable, so grabbing it to resize a pane silently kills keyboard
// input to whatever was focused -- and the same is true of any gap in a
// toolbar. Nothing reports it; the terminal just stops taking keys.
//
// The nil check is what makes this safe rather than rude. If the person moved
// focus somewhere on purpose -- the filter box, another tab's widget -- that
// focus is held by someone and this does nothing. It only ever fills a vacuum.
func (i *Instance) reclaimFocus() {
	if i.closed.Load() || i.mount.Focus == nil {
		return
	}
	c := i.canvas()
	if c == nil || c.Focused() != nil {
		return
	}
	// Only the instance the person is actually looking at. A backgrounded tab
	// answering a vacuum would steal keys from the visible one.
	if i.win == nil && i.shell.tabs.Selected() != i.tab {
		return
	}
	c.Focus(i.mount.Focus)
}

// notifyLayout is a max-layout that reports every layout pass. It exists
// because fyne.Container has no resize callback and container.Split has no
// drag callback, so a pane resize is otherwise unobservable from here.
type notifyLayout struct {
	onLayout func()
}

// Compile-time proof the interface is satisfied. This file cannot be built in
// the container it is written in, so the assertion is the check.
var _ fyne.Layout = (*notifyLayout)(nil)

func (l *notifyLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Resize(size)
		o.Move(fyne.NewPos(0, 0))
	}
	if l.onLayout != nil {
		l.onLayout()
	}
}

func (l *notifyLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var min fyne.Size
	for _, o := range objects {
		min = min.Max(o.MinSize())
	}
	return min
}

func (i *Instance) canvas() fyne.Canvas {
	if i.win != nil {
		return i.win.Canvas()
	}
	if i.shell.win != nil {
		return i.shell.win.Canvas()
	}
	return nil
}

// settle drives an instance into its new home: it tells the applet which canvas
// it is on, then waits for the window to reach its real size and for keyboard
// focus to actually land.
//
// Called after every mount and every move.
func (i *Instance) settle() {
	c := i.canvas()
	if c == nil {
		return
	}
	if i.mount.OnCanvasChange != nil {
		i.mount.OnCanvasChange(c)
	}
	i.step(0, fyne.Size{})
}

// step is one pass of the settle loop. It polls rather than hooking an event
// because neither of the two things it waits for reports itself.
//
// Focus: Canvas.Focus is allowed to do nothing and report success --
// FocusManager.Focus returns true without focusing when the object or any
// ancestor is not yet visible, which is the normal state just after
// Window.Show. Canvas.Focused() is the only honest signal that it landed.
//
// Size: a window is laid out on a later driver pass than the one that shows
// it, so the canvas reports the content's MINIMUM size first and its real size
// afterwards. Two consecutive equal readings is the signal that geometry has
// stopped moving; OnPlaced fires once, then.
func (i *Instance) step(attempt int, lastSize fyne.Size) {
	if i.closed.Load() {
		return
	}
	c := i.canvas()
	if c == nil {
		return
	}

	// An overlay on the canvas OWNS focus while it is up. Canvas.focusManager
	// returns the top overlay's manager, and that is the one the driver asks
	// who should receive keys -- so focusing content underneath a dialog puts
	// the object in the CONTENT manager, which Canvas.Focused() is not
	// reading. The call looks like it worked and the widget never sees a
	// keystroke. Waiting is the only correct move; asserting focus now would
	// be asserting it into a manager nobody is consulting.
	blocked := len(c.Overlays().List()) > 0

	focused := i.mount.Focus == nil
	if !focused && !blocked {
		if c.Focused() != i.mount.Focus {
			c.Focus(i.mount.Focus)
		}
		focused = c.Focused() == i.mount.Focus
	}

	size := c.Size()
	sized := size.Width > 0 && size == lastSize

	// A dialog the user is reading is a legitimate wait and must not burn the
	// budget, so blocked attempts are counted separately -- but they ARE
	// counted, because an overlay that never closes must not spin forever.
	budget := settleAttempts
	if blocked {
		budget = blockedAttempts
	}

	if (focused && sized) || attempt >= budget {
		if !focused && i.mount.Focus != nil {
			// Silence here is the whole bug this guards against: the loop
			// gives up, the applet is placed, and the only symptom is a
			// terminal that ignores the keyboard. Say what actually holds
			// focus, and whether an overlay was in the way.
			fyne.LogError(fmt.Sprintf(
				"shell: %q never took keyboard focus after %d attempts (focused=%T, overlays=%d)",
				i.info.Title, attempt, c.Focused(), len(c.Overlays().List())), nil)
		}
		// On giving up, place anyway: a terminal at the wrong number of
		// columns is worse than one that was never focused, and the
		// user's first click fixes focus.
		if i.mount.OnPlaced != nil {
			i.mount.OnPlaced()
		}
		return
	}

	// While blocked, hold lastSize rather than adopting the current one, so
	// the two-equal-readings size check is not satisfied by an overlay-era
	// measurement that the layout will change again once the dialog closes.
	next := size
	if blocked {
		next = lastSize
	}
	time.AfterFunc(settleInterval, func() {
		fyne.Do(func() { i.step(attempt+1, next) })
	})
}

// Roughly one second of trying, which covers a window that is slow to map.
//
// blockedAttempts is the longer budget used while an overlay holds focus: a
// person reading a dialog is a wait measured in seconds, not milliseconds, and
// giving up before they dismiss it means the applet underneath is placed with
// no focus and stays that way. Fifteen seconds, then it reports and moves on.
const (
	settleAttempts  = 20
	blockedAttempts = 300
	settleInterval  = 50 * time.Millisecond
)

func (i *Instance) togglePlacement() {
	if i.win == nil {
		i.Detach()
		return
	}
	i.Redock()
}

// Detach moves the instance into a window of its own.
//
// Nothing about the applet changes -- the same root object is displayed
// somewhere else. All windows share one driver and one UI goroutine, so this
// buys screen space and not parallelism.
func (i *Instance) Detach() {
	if i.win != nil || i.closed.Load() {
		return
	}
	// Give up focus on the canvas being left. Two canvases both believing
	// they hold this object is not fatal, but it means the main window
	// calls FocusGained/FocusLost on a widget that is no longer in its
	// tree every time it is raised.
	if c := i.shell.win.Canvas(); c != nil && c.Focused() == i.mount.Focus {
		c.Unfocus()
	}

	// Remove before add: a CanvasObject in two containers lays out in
	// neither.
	if i.tab != nil {
		i.shell.releaseTab(i.tab)
		i.tab = nil
	}

	w := i.shell.app.NewWindow(i.info.Title)
	w.Resize(detachedWindowSize)
	w.SetContent(i.root)

	// Lay the content out at the size it is ABOUT to have. SetContent has
	// just sized it to its minimum, and the terminal's resize is debounced
	// by 150ms -- long enough that the minimum would be what reaches the
	// far end if the real geometry arrived late. Doing this now means the
	// debounce restarts on the right numbers and the remote never hears
	// about the minimum at all. OnPlaced is the belt to this braces.
	i.root.Resize(detachedWindowSize)

	// The close box re-docks rather than destroying the session. Closing a
	// detached window and losing a live terminal is the kind of surprise
	// that makes people stop detaching things; the close button in the
	// instance bar is the way to actually end it.
	w.SetCloseIntercept(func() { i.Redock() })

	i.win = w
	i.shell.reg.SetPlacement(i.info.ID, Detached)
	i.place.SetIcon(theme.ViewRestoreIcon())
	i.shell.refreshSummary()
	w.Show()

	// Ask the window manager for keyboard focus, not just the canvas.
	//
	// These are two different things and only one of them was being set.
	// Canvas.Focus decides which widget receives keys IF the window is the
	// one the OS is sending keys to; on a click-to-focus desktop a newly
	// shown window is often not, so a correctly focused terminal sat there
	// receiving nothing. The tell was that the FIRST click on the detached
	// terminal did nothing and the SECOND worked -- the first was spent
	// giving the window focus and never reached the app.
	//
	// Note this is a no-op under Wayland by Fyne's own design, where the
	// compositor decides; there the first click remains the fallback.
	w.RequestFocus()

	// After Show, so the canvas exists. It is very likely NOT mapped yet --
	// settle retries until focus actually lands.
	i.settle()
	i.startRepaint()
}

// Redock brings a detached instance back into the tab strip.
func (i *Instance) Redock() {
	if i.win == nil || i.closed.Load() {
		return
	}
	i.stopRepaint()

	w := i.win
	i.win = nil

	if c := w.Canvas(); c != nil && c.Focused() == i.mount.Focus {
		c.Unfocus()
	}

	// Release the object from the window before handing it to the tab.
	w.SetContent(widget.NewLabel(""))
	w.SetCloseIntercept(nil)
	w.Close()

	i.tab = container.NewTabItem(i.info.Title, i.root)
	i.shell.tabs.Append(i.tab)
	i.shell.tabs.Select(i.tab)
	i.shell.reg.SetPlacement(i.info.ID, Docked)
	i.place.SetIcon(theme.ViewFullScreenIcon())
	i.shell.refreshSummary()
	i.shell.win.RequestFocus()

	// Explicitly, rather than relying on the Select above to fire
	// OnSelected: selecting a tab that is already current does not, and a
	// redocked instance is often the only tab.
	i.settle()
}

// CloseAll tears everything down: every tab and every detached window. It is
// both the "Close All Tabs" action and what the main window's close intercept
// calls on the way out. Instances are copied out first because Close mutates
// the map.
func (s *Shell) CloseAll() {
	for _, inst := range s.instances() {
		inst.Close()
	}
}

// focusTab hands keyboard focus to the applet that asked for it. Without this a
// terminal stops accepting input the moment you visit another tab and come
// back, and the terminal gets the blame.
func (s *Shell) focusTab(t *container.TabItem) {
	if inst := s.instanceFor(t); inst != nil {
		inst.settle()
	}
}

// instanceFor maps a tab back to the instance that owns it, or nil.
func (s *Shell) instanceFor(t *container.TabItem) *Instance {
	if t == nil {
		return nil
	}
	for _, inst := range s.byID {
		if inst.tab == t {
			return inst
		}
	}
	return nil
}

// TabCount is how many instances are open, docked and detached together.
// Detached ones count because they are still sessions somebody has to close.
func (s *Shell) TabCount() int { return len(s.byID) }

// Current is the instance whose tab is selected, or nil when nothing is docked.
// A detached instance is never "current" -- the shell cannot know which window
// the window manager is pointing at, and guessing would close the wrong one.
func (s *Shell) Current() *Instance { return s.instanceFor(s.tabs.Selected()) }

// CloseCurrent closes the selected tab. No-op when nothing is docked.
func (s *Shell) CloseCurrent() {
	if inst := s.Current(); inst != nil {
		inst.Close()
	}
}

// CloseOthers closes every instance except keep, including detached ones --
// "close the others" means the others, and a window left behind after that
// would be the one thing the person cannot see to close.
//
// The list is taken first because Close mutates the map underneath it.
func (s *Shell) CloseOthers(keep *Instance) {
	for _, inst := range s.instances() {
		if inst != keep {
			inst.Close()
		}
	}
}

// Busy is every open instance that answers Busy with a reason, docked and
// detached alike, in display order.
//
// Display order rather than map order: this is read as a list, and a list
// whose rows reshuffle every time it is shown is one nobody trusts to be the
// same list. It walks the registry for that ordering and looks each instance
// up, so an instance that has already been removed simply does not appear.
//
// UI goroutine only -- it reads byID and calls into the host.
func (s *Shell) Busy() []BusyInstance {
	var out []BusyInstance
	for _, info := range s.reg.All() {
		inst := s.byID[info.ID]
		if inst == nil || inst.mount.Busy == nil {
			continue
		}
		reason := inst.mount.Busy()
		if reason == "" {
			continue
		}
		out = append(out, BusyInstance{
			Kind:      info.Kind,
			Title:     info.Title,
			Reason:    reason,
			Placement: info.Placement,
		})
	}
	return out
}

// instances is a snapshot of every open instance, safe to iterate while
// closing.
func (s *Shell) instances() []*Instance {
	out := make([]*Instance, 0, len(s.byID))
	for _, inst := range s.byID {
		out = append(out, inst)
	}
	return out
}

func (s *Shell) refreshSummary() {
	if sum := s.reg.Summary(); sum != "" {
		s.summary.SetText(sum)
		return
	}
	s.summary.SetText("Nothing open — use Launch")
}
