// internal/ui/terminal_containers.go
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
)

// HybridScrollContainer shows scroll bars but forwards mouse events to terminal
type HybridScrollContainer struct {
	*container.Scroll
	terminal *NativeTerminalWidget
}

// NewHybridScrollContainer creates a new hybrid scroll container
func NewHybridScrollContainer(terminal *NativeTerminalWidget) *HybridScrollContainer {
	terminal.bgLayer = newBGLayer(terminal)
	terminal.baseBG = canvas.NewRectangle(terminal.termBG())
	content := container.NewStack(terminal.baseBG, terminal.bgLayer, terminal.textGrid) // base fill, SGR/selection bg, glyphs on top
	baseScroll := container.NewScroll(content)
	baseScroll.SetMinSize(fyne.NewSize(600, 400))
	// No native scrollbar: scrolling is virtual (the TextGrid is always sized to
	// the viewport, so this Scroll never overflows). The history-aware
	// VirtualScrollbar in the Border gutter is the visible bar. See
	// OptimizeForVirtualScrolling for the full rationale.
	baseScroll.Direction = container.ScrollNone

	h := &HybridScrollContainer{
		Scroll:   baseScroll,
		terminal: terminal,
	}

	dprintf("NewHybridScrollContainer: Created with terminal %p\n", terminal)
	return h
}

// Mouse events are forwarded UNTRANSLATED. Every fyne.PointEvent carries both a
// widget-local Position and a canvas-absolute AbsolutePosition, and the hit test
// uses the absolute one (see gridCellAtAbs) precisely so that no code has to
// know which widget the driver chose to deliver the event to. Translating the
// local position between widget spaces here is how the offset got applied twice.

// pinOffset forces this container's scroll offset back to zero.
//
// Scrolling here is VIRTUAL: the grid is redrawn with different content rather
// than moved. A non-zero Offset therefore displaces the glyphs without moving
// anything the terminal knows about, and Direction=ScrollNone does NOT prevent
// it -- that only suppresses the scroll BARS, while the renderer still does
// Content.Move(-Offset) unconditionally. The slack is real because the grid
// carries a sentinel row (see setStyledRows): content MinSize is
// (rows+1)*cellHeight, which exceeds the viewport by up to a full row, so the
// container has genuine, invisible room to scroll into.
func (h *HybridScrollContainer) pinOffset() {
	if h.Scroll.Offset.IsZero() {
		return
	}
	// Field only, no Refresh: container.Scroll reads Offset during layout
	// (Content.Move(-Offset)), so zeroing it is enough and the next paint picks
	// it up. Refreshing a container from the paint path is a per-frame side
	// effect on the widget tree, which is not a thing to do casually in a
	// toolkit where focus and renderer caches live in that same tree.
	h.Scroll.Offset = fyne.NewPos(0, 0)
	dprintf("pinOffset: reset non-zero scroll offset under virtual scrolling\n")
}

// Forward mouse events to terminal for selection
func (h *HybridScrollContainer) MouseDown(event *desktop.MouseEvent) {
	// Take keyboard focus on any click in the terminal body, before anything
	// else and for either button.
	//
	// NativeTerminalWidget.MouseDown does this too and never runs: the driver
	// dispatches a mouse event to the DEEPEST fyne.Mouseable under the pointer,
	// and that is this container, not the terminal that owns it. So the
	// terminal's own focus-on-click has been dead code for as long as it has
	// been hosted in one of these -- which is why, when something else took
	// focus away, clicking the terminal did not bring it back and only
	// switching tabs did.
	if h.terminal != nil {
		h.terminal.GrabFocus()
	}

	// Right-click is handled on MouseUp (context menu); don't start a selection,
	// which would clear any selection the user is about to Copy.
	if event.Button == desktop.MouseButtonSecondary {
		return
	}
	dprintf("HybridScrollContainer.MouseDown: Forwarding to terminal\n")
	if h.terminal != nil {
		// Any offset here is stale displacement, not scroll state; clear it
		// before a position is resolved against the grid.
		h.pinOffset()

		h.terminal.isSelecting = true
		if h.terminal.selection != nil {
			h.terminal.selection.HandleMouseDown(event)
		}
		h.terminal.updatePending.Store(true)
	}
}

func (h *HybridScrollContainer) MouseUp(event *desktop.MouseEvent) {
	// Right-click: show the Copy/Paste context menu at the cursor, leaving any
	// existing selection intact so Copy can act on it.
	if event.Button == desktop.MouseButtonSecondary {
		dprintf("HybridScrollContainer.MouseUp: Secondary -> context menu\n")
		if h.terminal != nil {
			h.terminal.ShowContextMenuAt(event.AbsolutePosition)
		}
		return
	}
	dprintf("HybridScrollContainer.MouseUp: Forwarding to terminal\n")
	if h.terminal != nil {
		h.terminal.isSelecting = false
		if h.terminal.selection != nil {
			h.terminal.selection.HandleMouseUp(event)
		}
	}
}

func (h *HybridScrollContainer) Dragged(event *fyne.DragEvent) {
	dprintf("HybridScrollContainer.Dragged: Forwarding to terminal\n")
	if h.terminal != nil && h.terminal.isSelecting {
		if h.terminal.selection != nil {
			h.terminal.selection.HandleDrag(event.AbsolutePosition, event.Position)
		}
		h.terminal.updatePending.Store(true)
	}
}

func (h *HybridScrollContainer) DragEnd() {
	dprintf("HybridScrollContainer.DragEnd: Forwarding to terminal\n")
	if h.terminal != nil {
		h.terminal.isSelecting = false
		if h.terminal.selection != nil {
			h.terminal.selection.HandleMouseUp(&desktop.MouseEvent{
				Button: desktop.MouseButtonPrimary,
			})
		}
	}
}

// Handle scroll wheel events
func (h *HybridScrollContainer) Scrolled(event *fyne.ScrollEvent) {
	dprintf("HybridScrollContainer.Scrolled: DY=%.2f\n", event.Scrolled.DY)

	// Let terminal handle scroll events first
	handled := h.terminal.handleScrollEvent(event)

	// Deliberately NOT forwarded to h.Scroll.Scrolled on the unhandled path.
	// handleScrollEvent returns false in alternate-screen mode (vim, top, a
	// pager), where the wheel belongs to the remote application - and forwarding
	// it let the container consume the sentinel row's slack, sliding the grid up
	// by a fraction of a row with no scroll bar to show it. Every subsequent
	// click then resolved against a row boundary that was no longer where the
	// glyphs were, until a re-layout (a tab switch) happened to reset it.
	if !handled {
		dprintf("HybridScrollContainer.Scrolled: Terminal didn't handle; dropping (virtual scrolling)\n")
	} else {
		dprintf("HybridScrollContainer.Scrolled: Terminal handled scroll event\n")
	}
	h.pinOffset()
}

// SCROLL BAR POSITION MANAGEMENT

func (h *HybridScrollContainer) UpdateScrollPosition() {
	if h.terminal.screen.IsUsingAlternate() {
		// In alternate screen, no virtual scrolling - position at top
		h.ScrollToTop()
		dprintf("UpdateScrollPosition: Alternate screen, scrolled to top\n")
		return
	}

	if h.terminal.IsInHistoryMode() {
		// In history mode - position scroll bar based on history position
		var scrollPercentage float32 = 0.5 // Mid-position as placeholder

		h.setScrollBarPosition(scrollPercentage)
		dprintf("UpdateScrollPosition: History mode, percentage=%.2f\n", scrollPercentage)
	} else {
		// Normal mode - scroll to bottom
		h.ScrollToBottom()
		dprintf("UpdateScrollPosition: Normal mode, scrolled to bottom\n")
	}
}

func (h *HybridScrollContainer) setScrollBarPosition(percentage float32) {
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 1 {
		percentage = 1
	}

	// Under virtual scrolling the container itself never scrolls: position is
	// expressed by redrawing the grid with different lines, and the visible bar
	// is the VirtualScrollbar in the Border gutter. Moving the container's
	// offset here would displace the glyphs out from under the hit test, so the
	// only correct offset is zero. The percentage is retained for the callers
	// and the log.
	h.pinOffset()
	dprintf("setScrollBarPosition: virtual scrolling - offset pinned at 0 (requested %.1f%%)\n",
		percentage*100)
}

func (h *HybridScrollContainer) ScrollToBottom() {
	// The bottom of the buffer is a CONTENT position, reached by redrawing the
	// grid, not by offsetting the container. Offsetting it here is what put the
	// glyphs half a row above where the hit test believed they were - the
	// sentinel row leaves exactly that much slack for it to slide into.
	h.pinOffset()
	dprintf("ScrollToBottom: virtual scrolling - offset pinned at 0\n")
}

func (h *HybridScrollContainer) ScrollToTop() {
	h.pinOffset()
	dprintf("ScrollToTop: virtual scrolling - offset pinned at 0\n")
}

func (h *HybridScrollContainer) GetScrollPosition() float32 {
	contentSize := h.terminal.textGrid.Size()
	containerSize := h.Scroll.Size()

	if contentSize.Height <= containerSize.Height {
		return 0.0 // No scrolling possible
	}

	maxScrollY := contentSize.Height - containerSize.Height
	currentScrollY := h.Scroll.Offset.Y

	if maxScrollY <= 0 {
		return 0.0
	}

	percentage := currentScrollY / maxScrollY
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 1 {
		percentage = 1
	}

	return percentage
}

// VIRTUAL SCROLLING INTEGRATION

func (h *HybridScrollContainer) SyncWithVirtualScroll(viewport VirtualScrollState) {
	// Only sync if in normal mode with virtual scrolling
	if h.terminal.screen.IsUsingAlternate() {
		return
	}

	// Calculate scroll position based on viewport
	var scrollPercentage float32 = 0
	if viewport.maxScroll > 0 {
		scrollPercentage = float32(viewport.scrollOffset) / float32(viewport.maxScroll)
	}

	h.setScrollBarPosition(scrollPercentage)
	dprintf("SyncWithVirtualScroll: offset=%d/%d, percentage=%.2f\n",
		viewport.scrollOffset, viewport.maxScroll, scrollPercentage)
}

// CONFIGURATION

func (h *HybridScrollContainer) SetScrollBarVisibility(horizontal, vertical bool) {
	if horizontal && vertical {
		h.Scroll.Direction = container.ScrollBoth
	} else if horizontal {
		h.Scroll.Direction = container.ScrollHorizontalOnly
	} else if vertical {
		h.Scroll.Direction = container.ScrollVerticalOnly
	} else {
		h.Scroll.Direction = container.ScrollNone
	}

	h.Scroll.Refresh()
	dprintf("SetScrollBarVisibility: horizontal=%v, vertical=%v\n", horizontal, vertical)
}

func (h *HybridScrollContainer) IsScrollBarVisible() (horizontal, vertical bool) {
	switch h.Scroll.Direction {
	case container.ScrollBoth:
		return true, true
	case container.ScrollHorizontalOnly:
		return true, false
	case container.ScrollVerticalOnly:
		return false, true
	case container.ScrollNone:
		return false, false
	default:
		return false, false
	}
}

func (h *HybridScrollContainer) OptimizeForVirtualScrolling() {
	// No native Fyne scrollbar. The terminal scrolls VIRTUALLY: the TextGrid is
	// always resized to exactly the viewport, so the inner container.Scroll never
	// overflows and its offset code (setScrollBarPosition/ScrollToBottom/etc)
	// should be a no-op -- it was NOT, because the sentinel row in setStyledRows
	// pushes the content's MinSize one cell height past the viewport, giving the
	// container real (and invisible) room to scroll. Those methods now pin the
	// offset at zero instead of setting it; see pinOffset. Leaving Direction=ScrollVerticalOnly made Fyne paint its OWN vertical
	// scrollbar over the right edge - the theme-accent bar that auto-hides on
	// hover-out and, because it only sees the viewport-sized content (never the
	// scrollback history), stays full-height and refuses to shrink after e.g.
	// `ls -l`. ScrollNone suppresses that bar entirely; the draggable, history-
	// aware VirtualScrollbar in the Border gutter (driven by updateUnifiedScrollBar)
	// is the single source of truth for scrollback position. Wheel events still
	// reach handleScrollEvent and drags still reach ScrollToFraction, so nothing
	// scroll-related depends on the native bar.
	h.SetScrollBarVisibility(false, false)

	// Set reasonable minimum size
	h.Scroll.SetMinSize(fyne.NewSize(400, 300))

	dprintf("OptimizeForVirtualScrolling: Configured for virtual scrolling (native scrollbar disabled)\n")
}

func (h *HybridScrollContainer) UpdateContentSize(width, height float32) {
	newSize := fyne.NewSize(width, height)

	// Only update if size actually changed
	currentSize := h.terminal.textGrid.Size()
	if currentSize.Width != width || currentSize.Height != height {
		h.terminal.textGrid.Resize(newSize)
		h.Scroll.Refresh()

		dprintf("UpdateContentSize: Updated to %.1fx%.1f\n", width, height)
	}
}

// DEBUGGING AND MONITORING