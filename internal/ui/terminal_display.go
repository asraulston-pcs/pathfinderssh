// internal/ui/terminal_display.go
// Enhanced terminal_display.go with comprehensive debugging
package ui

import (
	"image/color"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scottpeterman/pathfinderssh/internal/gopyte"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

type VirtualScrollState struct {
	totalLines    int     // Total content lines available
	visibleLines  int     // Lines visible in viewport
	scrollOffset  int     // Current scroll position (line number)
	maxScroll     int     // Maximum scroll position
	contentHeight float32 // Height of all content if rendered
}

// DEBUG: Add detailed buffer state logging
func (t *NativeTerminalWidget) logBufferState(context string) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	allLines := t.screen.GetDisplay()
	historySize := t.screen.GetHistorySize()
	isAlternate := t.screen.IsUsingAlternate()
	isViewingHistory := t.screen.IsViewingHistory()

	// Get additional state from WideCharScreen
	historyPos := t.screen.GetHistoryPos()
	maxHistoryPos := t.screen.GetMaxHistoryPos()
	totalContentLines := historySize + t.rows

	dlogf("=== BUFFER STATE DEBUG [%s] ===", context)
	dlogf("Terminal dimensions: %dx%d (cols x rows)", t.cols, t.rows)
	dlogf("Display lines returned: %d", len(allLines))
	dlogf("History size: %d lines", historySize)
	dlogf("History position: %d/%d", historyPos, maxHistoryPos)
	dlogf("Total content lines: %d", totalContentLines)
	dlogf("Using alternate screen: %v", isAlternate)
	dlogf("Viewing history: %v", isViewingHistory)

	// Show actual available scrollable area
	if !isAlternate {
		maxScrollableLines := historySize + t.rows
		actualViewableLines := len(allLines)
		scrollableRatio := float64(actualViewableLines) / float64(maxScrollableLines) * 100

		dlogf("SCROLL ANALYSIS:")
		dlogf("  - Max scrollable content: %d lines", maxScrollableLines)
		dlogf("  - Actually viewable: %d lines (%.1f%%)", actualViewableLines, scrollableRatio)
		dlogf("  - Potentially missing: %d lines", maxScrollableLines-actualViewableLines)

		if actualViewableLines < maxScrollableLines {
			dlogf("  - WARNING: LIMITED VIEWPORT DETECTED!")
		}
	}

	// Debug viewport calculation in WideCharScreen
	t.screen.DebugViewportState()

	dlogf("================================")
}

// Main redraw function with enhanced debugging
func (t *NativeTerminalWidget) performRedrawDirect() {
	// Log buffer state before processing
	t.logBufferState("BEFORE_REDRAW")

	readStart := time.Now()
	t.mutex.RLock()

	// Get display data
	allLines := t.screen.GetDisplay()
	allAttrs := t.screen.GetAttributes()
	isUsingAlternate := t.screen.IsUsingAlternate()

	t.mutex.RUnlock()

	dlogf("performRedrawDirect: engine read %v -> %d lines, alternate=%v",
		time.Since(readStart), len(allLines), isUsingAlternate)

	// Route to appropriate renderer
	renderStart := time.Now()
	if isUsingAlternate {
		t.renderAlternateScreen(allLines, allAttrs)
	} else {
		shouldAutoScroll := !t.IsInHistoryMode()
		t.renderNormalMode(allLines, allAttrs, shouldAutoScroll)
	}
	dlogf("performRedrawDirect: render %v (alternate=%v)",
		time.Since(renderStart), isUsingAlternate)

	// Log buffer state after processing
	t.logBufferState("AFTER_REDRAW")
}

// Alternate screen renderer (vim, htop, less)
func (t *NativeTerminalWidget) renderAlternateScreen(allLines []string, allAttrs [][]gopyte.Attributes) {
	dlogf("ALTERNATE: Rendering full screen mode with %d lines", len(allLines))

	// Size TextGrid to exact screen dimensions
	screenSize := fyne.NewSize(
		float32(t.cols)*t.charWidth,
		float32(t.rows)*t.charHeight,
	)

	t.textGrid.Resize(screenSize)

	// Prepare display lines
	displayLines := make([]string, t.rows)
	for i := 0; i < t.rows; i++ {
		if i < len(allLines) {
			displayLines[i] = allLines[i]
		} else {
			displayLines[i] = ""
		}
	}

	// Pad lines to exact column width
	for i := range displayLines {
		runes := []rune(displayLines[i])
		if len(runes) < t.cols {
			padding := strings.Repeat(" ", t.cols-len(runes))
			displayLines[i] = displayLines[i] + padding
		} else if len(runes) > t.cols {
			displayLines[i] = string(runes[:t.cols])
		}
	}

	// Place cursor
	cursorX, cursorY := t.screen.GetCursor()
	if cursorY >= 0 && cursorY < len(displayLines) && cursorX >= 0 && cursorX < t.cols {
		t.placeCursorInLine(&displayLines[cursorY], cursorX)
		dlogf("ALTERNATE: Cursor at (%d,%d)", cursorX, cursorY)
	}

	// Build styled rows and refresh once (see setStyledRows).
	t.setStyledRows(displayLines, allAttrs, nil)

	dlogf("ALTERNATE: Rendered %d lines", len(displayLines))
}

// Enhanced normal mode renderer with detailed viewport debugging
func (t *NativeTerminalWidget) renderNormalMode(allLines []string, allAttrs [][]gopyte.Attributes, shouldAutoScroll bool) {
	dlogf("NORMAL: Rendering %d lines, autoScroll=%v", len(allLines), shouldAutoScroll)

	if len(allLines) == 0 {
		t.textGrid.SetText("")
		return
	}

	// Calculate viewport with detailed logging
	viewport := t.calculateVirtualViewport(allLines)
	t.logViewportCalculation(viewport, len(allLines))

	// Size TextGrid - POTENTIAL ISSUE: This might be too restrictive
	viewportSize := fyne.NewSize(
		float32(t.cols)*t.charWidth,
		float32(viewport.visibleLines)*t.charHeight,
	)
	t.textGrid.Resize(viewportSize)

	dlogf("NORMAL: TextGrid resized to %.1fx%.1f (for %d visible lines)",
		viewportSize.Width, viewportSize.Height, viewport.visibleLines)

	// Extract visible content
	visibleLines := t.extractVisibleContent(allLines, viewport)

	// Place cursor if visible
	cursorX, cursorY := t.screen.GetCursor()
	adjustedCursorY := t.adjustCursorForViewport(cursorX, cursorY, viewport, len(allLines))

	if adjustedCursorY >= 0 && adjustedCursorY < len(visibleLines) && cursorX >= 0 && cursorX < t.cols && !t.IsInHistoryMode() {
		t.placeCursorInLine(&visibleLines[adjustedCursorY], cursorX)
		dlogf("NORMAL: Cursor at (%d,%d) in viewport", cursorX, adjustedCursorY)
	}

	// Build styled rows and refresh once (see setStyledRows) - avoids the
	// uncolored intermediate frame that SetText()+applyColors() produced.
	var visibleAttrs [][]gopyte.Attributes
	if len(allAttrs) > 0 {
		visibleAttrs = t.extractVisibleAttributes(allAttrs, viewport)
	}
	var sel *selRange
	if t.selection != nil {
		topAbs := t.screen.GetViewportStart() + viewport.scrollOffset
		sel = t.selection.toRange(viewport, topAbs)
	}
	t.setStyledRows(visibleLines, visibleAttrs, sel)

	// Keep the overlay in lockstep with the text we just painted (it does not
	// composite under the font-size theme override, but stays correct for any
	// path that can display it).
	if t.bgLayer != nil {
		if len(visibleAttrs) == 0 {
			visibleAttrs = make([][]gopyte.Attributes, viewport.visibleLines)
			for i := range visibleAttrs {
				visibleAttrs[i] = make([]gopyte.Attributes, t.cols)
			}
		}
		t.bgLayer.Update(visibleAttrs, sel)
	}

	dlogf("NORMAL: Rendered viewport lines %d-%d of %d total",
		viewport.scrollOffset, viewport.scrollOffset+viewport.visibleLines-1, len(allLines))
}

// Enhanced viewport calculation with detailed debugging
func (t *NativeTerminalWidget) calculateVirtualViewport(allLines []string) VirtualScrollState {
	totalLines := len(allLines)
	visibleLines := t.rows

	// DEBUG: Check if t.rows is limiting us
	dlogf("VIEWPORT CALC: t.rows=%d, but do we have container space for more?", t.rows)

	if visibleLines <= 0 {
		visibleLines = 24
		dlogf("VIEWPORT CALC: t.rows was %d, defaulting to %d", t.rows, visibleLines)
	}

	// Check if we're artificially limiting the viewport
	historySize := t.screen.GetHistorySize()
	theoreticalMax := historySize + t.rows

	dlogf("VIEWPORT CALC ANALYSIS:")
	dlogf("  - totalLines returned by GetDisplay(): %d", totalLines)
	dlogf("  - t.rows (terminal height): %d", t.rows)
	dlogf("  - historySize: %d", historySize)
	dlogf("  - theoretical max content: %d", theoreticalMax)
	dlogf("  - visibleLines (viewport): %d", visibleLines)

	if totalLines > visibleLines*2 {
		dlogf("  - WARNING: LARGE BUFFER: %d lines available but viewport only %d", totalLines, visibleLines)
	}

	var scrollOffset int

	if t.IsInHistoryMode() {
		// History mode scrolling
		historySize := t.GetHistorySize()
		currentPos := 0
		if t.screen != nil && t.screen.HistoryScreen != nil {
			currentPos = t.screen.GetHistoryPos()
		}

		if totalLines <= visibleLines {
			scrollOffset = 0
		} else {
			maxScrollOffset := totalLines - visibleLines
			if historySize > 0 {
				scrollOffset = maxScrollOffset - ((currentPos * maxScrollOffset) / historySize)
			} else {
				scrollOffset = maxScrollOffset
			}

			if currentPos >= historySize {
				scrollOffset = 0
			}

			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > maxScrollOffset {
				scrollOffset = maxScrollOffset
			}
		}

		dlogf("HISTORY MODE: pos=%d/%d -> scrollOffset=%d", currentPos, historySize, scrollOffset)
	} else {
		// Normal mode: show bottom
		if totalLines <= visibleLines {
			scrollOffset = 0
		} else {
			scrollOffset = totalLines - visibleLines
		}
		dlogf("NORMAL MODE: scrollOffset=%d (showing bottom %d of %d)", scrollOffset, visibleLines, totalLines)
	}

	maxScroll := totalLines - visibleLines
	if maxScroll < 0 {
		maxScroll = 0
	}

	return VirtualScrollState{
		totalLines:    totalLines,
		visibleLines:  visibleLines,
		scrollOffset:  scrollOffset,
		maxScroll:     maxScroll,
		contentHeight: float32(totalLines) * t.charHeight,
	}
}

// New method to log viewport calculation details
func (t *NativeTerminalWidget) logViewportCalculation(viewport VirtualScrollState, totalLinesAvailable int) {
	dlogf("VIEWPORT RESULT:")
	dlogf("  - totalLines: %d", viewport.totalLines)
	dlogf("  - visibleLines: %d", viewport.visibleLines)
	dlogf("  - scrollOffset: %d", viewport.scrollOffset)
	dlogf("  - maxScroll: %d", viewport.maxScroll)
	dlogf("  - contentHeight: %.1f", viewport.contentHeight)
	dlogf("  - showing lines [%d-%d] of %d available",
		viewport.scrollOffset,
		viewport.scrollOffset+viewport.visibleLines-1,
		totalLinesAvailable)

	// Flag potential issues
	utilizationPercent := float64(viewport.visibleLines) / float64(viewport.totalLines) * 100
	if utilizationPercent < 50 {
		dlogf("  - WARNING: LOW UTILIZATION: Only showing %.1f%% of available content", utilizationPercent)
	}

	if viewport.visibleLines < t.rows {
		dlogf("  - WARNING: VIEWPORT SMALLER THAN TERMINAL: viewport=%d < t.rows=%d", viewport.visibleLines, t.rows)
	}
}

// Extract visible content from viewport
func (t *NativeTerminalWidget) extractVisibleContent(allLines []string, viewport VirtualScrollState) []string {
	visibleContent := make([]string, viewport.visibleLines)

	for i := 0; i < viewport.visibleLines; i++ {
		lineIndex := viewport.scrollOffset + i
		if lineIndex < len(allLines) {
			visibleContent[i] = allLines[lineIndex]
		} else {
			visibleContent[i] = ""
		}
	}

	return visibleContent
}

// Extract visible attributes
func (t *NativeTerminalWidget) extractVisibleAttributes(allAttrs [][]gopyte.Attributes, viewport VirtualScrollState) [][]gopyte.Attributes {
	if len(allAttrs) == 0 {
		return [][]gopyte.Attributes{}
	}

	visibleAttrs := make([][]gopyte.Attributes, viewport.visibleLines)
	for i := 0; i < viewport.visibleLines; i++ {
		lineIndex := viewport.scrollOffset + i
		if lineIndex < len(allAttrs) {
			visibleAttrs[i] = allAttrs[lineIndex]
		}
	}

	return visibleAttrs
}

// Adjust cursor position for viewport
func (t *NativeTerminalWidget) adjustCursorForViewport(cursorX, cursorY int, viewport VirtualScrollState, totalLines int) int {
	if t.IsInHistoryMode() {
		return -1 // Don't show cursor in history mode
	}

	if totalLines <= viewport.visibleLines {
		return cursorY
	} else {
		actualCursorLine := totalLines - viewport.visibleLines + cursorY
		if actualCursorLine >= viewport.scrollOffset && actualCursorLine < viewport.scrollOffset+viewport.visibleLines {
			return actualCursorLine - viewport.scrollOffset
		}
		return -1
	}
}

// Place cursor in line
func (t *NativeTerminalWidget) placeCursorInLine(line *string, cursorX int) {
	if line == nil || cursorX < 0 {
		return
	}

	currentLine := *line
	runes := []rune(currentLine)

	if cursorX < len(runes) {
		// Replace character at cursor position with block cursor
		runes[cursorX] = '█'
		*line = string(runes)
	} else if cursorX < t.cols {
		// Extend line with spaces and place cursor
		padLen := cursorX - len(runes)
		if padLen > 0 {
			padding := strings.Repeat(" ", padLen)
			*line = currentLine + padding + "█"
		} else {
			*line = currentLine + "█"
		}
	}
}

// Map gopyte color to Fyne color - now theme-aware
// setStyledRows updates the TextGrid to match the given lines/attributes while
// touching as little as possible. During typing only a couple of cells change, so
// rebuilding all rows and calling the top-level Refresh() (which repaints every row
// widget and flashes colored backgrounds) is the wrong tool. Instead we diff against
// the current grid and update only changed cells via SetCell, whose granular
// refreshCell path repaints just that cell's canvas objects.
//
// Two Fyne v2.6.2 quirks shape this:
//   - SetRow's refresh loop is a no-op (a "col > len" typo), so it can't be used.
//   - refreshCell refuses to refresh the last row object ("row >= len-1" guard), so
//     we keep one empty sentinel row below the content. It sits past the resized
//     grid height, so it's clipped and never visible, but it makes the real bottom
//     line (the shell prompt) refreshable.
func (t *NativeTerminalWidget) setStyledRows(lines []string, attrs [][]gopyte.Attributes, sel *selRange) {
	grid := t.textGrid

	cols := t.cols
	if cols <= 0 {
		for _, l := range lines {
			if n := len([]rune(l)); n > cols {
				cols = n
			}
		}
	}

	wantRows := len(lines) + 1 // +1 sentinel (see doc comment)

	// Can we diff in place, or has the structure changed (resize/scroll/first paint)?
	structureOK := len(grid.Rows) == wantRows
	if structureOK {
		for r := 0; r < len(lines); r++ {
			if len(grid.Rows[r].Cells) != cols {
				structureOK = false
				break
			}
		}
	}

	if !structureOK {
		// Full rebuild + one Refresh. Rare next to keystrokes, and the brief flash
		// is unnoticeable when the whole screen is turning over anyway.
		rebuildStart := time.Now()
		rows := make([]widget.TextGridRow, wantRows)
		for r, line := range lines {
			rows[r] = t.buildPaddedRow(line, attrAt(attrs, r), cols, r, sel)
		}
		rows[len(lines)] = widget.TextGridRow{} // sentinel
		grid.Rows = rows
		grid.Refresh()
		dlogf("setStyledRows: REBUILD path (have %d rows, want %d; cols=%d) in %v",
			len(grid.Rows), wantRows, cols, time.Since(rebuildStart))
		return
	}

	// Steady state: update only the cells that actually changed.
	diffStart := time.Now()
	changed := 0
	for r, line := range lines {
		runes := []rune(line)
		lineAttrs := attrAt(attrs, r)
		cur := grid.Rows[r].Cells
		for c := 0; c < cols; c++ {
			ch, fg, bg := t.gridCell(runes, lineAttrs, c, r, sel)
			if cur[c].Rune != ch || !cellHasColors(cur[c].Style, fg, bg) {
				grid.SetCell(r, c, widget.TextGridCell{Rune: ch, Style: makeStyle(fg, bg)})
				changed++
			}
		}
	}
	dlogf("setStyledRows: DIFF path updated %d of %d cells in %v",
		changed, len(lines)*cols, time.Since(diffStart))
}

// cellColors resolves a cell's foreground and background, with the text selection
// taking precedence over any SGR background. r/c are viewport-local coordinates,
// matching selRange. Selection is keyed on the cell position, not on whether the
// cell carries an attribute, so it highlights blank cells and the blank tail of
// short lines too. Backgrounds live on the TextGrid cell (CustomTextGridStyle.
// BGColor) rather than the separate overlay widget, because the overlay does not
// composite when the terminal is wrapped in a theme override for per-tab font
// size, whereas the grid always paints.
func (t *NativeTerminalWidget) cellColors(lineAttrs []gopyte.Attributes, c, r int, sel *selRange) (fg, bg color.Color) {
	if c >= 0 && c < len(lineAttrs) {
		attr := lineAttrs[c]

		// Bold handling, shared by normal and alternate (full-screen) modes so
		// the two render paths resolve color identically. Bold selects the
		// bright ANSI variant (standard terminal behavior: bold red -> bright
		// red). Bold black is the exception - "bright black" is the dim-grey
		// slot, near-invisible on a dark terminal, yet htop leans on bold-black
		// for primary process text, so promote it to a legible color instead.
		switch {
		case attr.Bold && attr.Fg == "black":
			if t.termIsDark() {
				fg = color.RGBA{0xff, 0xff, 0xff, 0xff} // white on dark
			} else {
				fg = t.termFG() // dark on light
			}
		case attr.Bold:
			fg = t.mapFg(brightenName(attr.Fg))
		default:
			fg = t.mapFg(attr.Fg)
		}

		bg = t.mapColor(attr.Bg)
	}
	if sel != nil && sel.contains(r, c) {
		bg = selectionColor
	}
	return fg, bg
}

// attrAt returns the attribute row at r, or nil if out of range.
func attrAt(attrs [][]gopyte.Attributes, r int) []gopyte.Attributes {
	if r >= 0 && r < len(attrs) {
		return attrs[r]
	}
	return nil
}

// makeStyle returns a style for the given colors, or nil when both are default.
// Returns an untyped-nil interface (not a typed nil) so equality checks behave.
func makeStyle(fg, bg color.Color) widget.TextGridStyle {
	if fg == nil && bg == nil {
		return nil
	}
	return &widget.CustomTextGridStyle{FGColor: fg, BGColor: bg}
}

// cellHasColors reports whether the cell's current style already represents exactly
// these fg/bg colors (and no other attributes), so an unchanged cell can be skipped
// without allocating a new style to compare against.
func cellHasColors(s widget.TextGridStyle, fg, bg color.Color) bool {
	if s == nil {
		return fg == nil && bg == nil
	}
	cs, ok := s.(*widget.CustomTextGridStyle)
	if !ok {
		return false
	}
	return cs.FGColor == fg && cs.BGColor == bg && cs.TextStyle == (fyne.TextStyle{})
}

// buildPaddedRow builds a row of exactly cols cells (padding short lines with
// spaces) so every row has a stable width to diff against. row is the
// viewport-local row index, used with sel to apply the selection background.
func (t *NativeTerminalWidget) buildPaddedRow(line string, lineAttrs []gopyte.Attributes, cols, row int, sel *selRange) widget.TextGridRow {
	runes := []rune(line)
	cells := make([]widget.TextGridCell, cols)
	for c := 0; c < cols; c++ {
		ch, fg, bg := t.gridCell(runes, lineAttrs, c, row, sel)
		cells[c] = widget.TextGridCell{Rune: ch, Style: makeStyle(fg, bg)}
	}
	return widget.TextGridRow{Cells: cells}
}

// gridCell resolves one cell's rune and colors, applying the double-width
// rules that neither the grid nor cellColors can express on its own.
//
// A continuation cell draws blank and paints NO background. Fyne's TextGrid
// renderer emits a background rectangle and a text object per cell in column
// order, so cell N+1's rectangle is drawn after cell N's glyph. A wide glyph
// is wider than one cell and overspills to the right, so an opaque background
// on the continuation cell repaints over the glyph's right half -- which is
// why a selection or an SGR background used to shred CJK text that rendered
// correctly when unhighlighted.
//
// The color for this column is not lost: bgLayer sits behind the transparent
// grid and paints the same selection/SGR rectangle across both columns.
func (t *NativeTerminalWidget) gridCell(runes []rune, lineAttrs []gopyte.Attributes, c, row int, sel *selRange) (ch rune, fg, bg color.Color) {
	ch = ' '
	if c < len(runes) {
		ch = runes[c]
	}
	fg, bg = t.cellColors(lineAttrs, c, row, sel)
	if ch == gopyte.ContinuationRune {
		return ' ', fg, nil
	}
	return ch, fg, bg
}

// colorMemo caches decoded colors for theme-INDEPENDENT inputs only: 24-bit
// truecolor ("#rrggbb") and 256-color cube/grayscale indices (16-255). These
// never change with the active theme, so caching them is always correct and
// needs no invalidation on theme switch. Named colors and 256-indices 0-15 are
// theme-aware and are deliberately resolved live (a cheap map hit anyway).
// Shared across tabs; guarded because mapColor can be reached from the bg layer
// renderer as well as the glyph path.
var (
	colorMemo   = make(map[string]color.Color)
	colorMemoMu sync.RWMutex
)

func memoColor(key string, compute func() color.Color) color.Color {
	colorMemoMu.RLock()
	c, ok := colorMemo[key]
	colorMemoMu.RUnlock()
	if ok {
		return c
	}
	c = compute()
	colorMemoMu.Lock()
	colorMemo[key] = c
	colorMemoMu.Unlock()
	return c
}

func (t *NativeTerminalWidget) mapColor(colorName string) color.Color {
	switch colorName {
	case "", "default":
		return nil
	case "brown":
		// gopyte emits "brown" for SGR 33; render it as the palette's yellow
		// so SGR 33 follows the active theme instead of falling through.
		return t.termPalette()["yellow"]
	}

	// 24-bit truecolor, encoded as "#rrggbb" by the SGR parser. Absolute (not
	// theme-dependent), so memoize: btop-class gradients re-emit the same few
	// hundred hex strings every frame, and parsing+allocating each one per
	// changed cell is the dominant per-frame cost under truecolor.
	if strings.HasPrefix(colorName, "#") {
		return memoColor(colorName, func() color.Color { return ParseHexColor(colorName) })
	}

	// 256-color, encoded as "color<N>" by the SGR parser. Indices 16-255 (cube +
	// grayscale) are absolute and memoized; 0-15 are theme-aware (resolved via
	// the live palette) so they are NOT cached - they must follow theme switches.
	if strings.HasPrefix(colorName, "color") {
		if n, err := strconv.Atoi(colorName[len("color"):]); err == nil {
			if n >= 16 {
				return memoColor(colorName, func() color.Color { return Xterm256Color(n) })
			}
			return t.termXterm256(n)
		}
	}

	// Named 16-color palette (theme-aware), including bright_* variants.
	if fyneColor, exists := t.termPalette()[colorName]; exists {
		return fyneColor
	}

	// Unknown name: treat as default (nil) so mapFg/cellBG fall back sensibly
	// rather than forcing every unmapped color to white.
	return nil
}

// mapFg resolves a glyph foreground color. Unlike mapColor it never returns nil:
// a default/unset foreground resolves to the terminal theme's default text color
// (termFG) so default text stays legible on the terminal's own background,
// independent of the application chrome.
func (t *NativeTerminalWidget) mapFg(colorName string) color.Color {
	if c := t.mapColor(colorName); c != nil {
		return c
	}
	return t.termFG()
}

// Call this method during scroll events to monitor what's happening
func (t *NativeTerminalWidget) debugScrollEvent(direction string, lines int) {
	dlogf("SCROLL DEBUG [%s by %d]:", direction, lines)
	t.logBufferState("DURING_SCROLL")

	historyPos := t.screen.GetHistoryPos()
	historySize := t.screen.GetHistorySize()
	dlogf("  - History position after scroll: %d/%d", historyPos, historySize)

	// Check if we're hitting limits
	if direction == "UP" && historyPos >= historySize {
		dlogf("  - WARNING: HIT TOP LIMIT: Cannot scroll up further")
	}
	if direction == "DOWN" && historyPos <= 0 {
		dlogf("  - WARNING: HIT BOTTOM LIMIT: Cannot scroll down further")
	}
}
