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
		// Forward directly to terminal's selection manager
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
			h.terminal.selection.HandleDrag(event.Position)
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

	if !handled {
		// Terminal didn't handle it, let scroll container handle it
		dprintf("HybridScrollContainer.Scrolled: Terminal didn't handle, passing to container\n")
		h.Scroll.Scrolled(event)
	} else {
		dprintf("HybridScrollContainer.Scrolled: Terminal handled scroll event\n")
	}
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

	// Get the actual content size from the TextGrid
	contentSize := h.terminal.textGrid.Size()
	containerSize := h.Scroll.Size()

	if contentSize.Height > containerSize.Height {
		maxScrollY := contentSize.Height - containerSize.Height
		scrollY := maxScrollY * percentage
		h.Scroll.Offset = fyne.NewPos(0, scrollY)
		h.Scroll.Refresh()

		dprintf("setScrollBarPosition: Set scroll to %.1f (%.1f%%), maxScroll=%.1f\n",
			scrollY, percentage*100, maxScrollY)
	} else {
		// Content fits in container, no scrolling needed
		h.Scroll.Offset = fyne.NewPos(0, 0)
		h.Scroll.Refresh()
	}
}

func (h *HybridScrollContainer) ScrollToBottom() {
	contentSize := h.terminal.textGrid.Size()
	containerSize := h.Scroll.Size()

	if contentSize.Height > containerSize.Height {
		maxScrollY := contentSize.Height - containerSize.Height
		h.Scroll.Offset = fyne.NewPos(0, maxScrollY)
		h.Scroll.Refresh()
		dprintf("ScrollToBottom: Scrolled to bottom (offset=%.1f)\n", maxScrollY)
	} else {
		h.Scroll.Offset = fyne.NewPos(0, 0)
		h.Scroll.Refresh()
		dprintf("ScrollToBottom: Content fits, no scroll needed\n")
	}
}

func (h *HybridScrollContainer) ScrollToTop() {
	h.Scroll.Offset = fyne.NewPos(0, 0)
	h.Scroll.Refresh()
	dprintf("ScrollToTop: Scrolled to top\n")
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
	// overflows and its offset code (setScrollBarPosition/ScrollToBottom/etc) is a
	// no-op. Leaving Direction=ScrollVerticalOnly made Fyne paint its OWN vertical
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
