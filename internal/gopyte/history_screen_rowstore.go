package gopyte

import (
	"strings"
)

// history_screen_rowstore.go
//
// Phase 2 of the scrollback rearchitecture: HistoryScreen's exported surface
// reimplemented on top of RowStore, with no changes required in the cli
// layer.
//
// The load-bearing trick is syncWindow. NativeScreen (and WideCharScreen
// above it) write cells through h.buffer[y][x] / h.attrs[y][x], so rather
// than rewriting them, buffer/attrs/cellWidths are repointed at the store's
// row slices whenever the visible window moves. Those aliases stay valid
// because Advance never copies rows -- it appends and moves an index. Writes
// through the embedded screen therefore land directly in the store, and
// there is exactly one copy of every cell.
//
// Two whole mechanisms disappear as a result:
//
//   - History as a separate container. Scrollback is just the rows below
//     base. GetHistoryLines walks [origin,base) instead of a linked list.
//   - The save/restore screen swap. Viewing scrollback moves viewPos, which
//     is a read offset. There is no saved copy to fall out of sync with the
//     live screen, so renderHistoryView, saveCurrentScreen and
//     restoreCurrentScreen have no replacement here -- they are not needed.
//
// Behavior is held to characterize_test.go, with one deliberate divergence
// noted at ScrollUp.

// HistoryScreen extends NativeScreen with scrollback backed by a RowStore.
type HistoryScreen struct {
	NativeScreen

	store   *RowStore
	viewPos int  // rows above base currently displayed; 0 = live
	viewing bool // true when viewPos > 0

	// cellWidths aliases the store rows for the visible window, kept for
	// compatibility with WideCharScreen. It is a view, never a second copy.
	cellWidths [][]int
}

// HistoryLine is retained for API compatibility with consumers that read
// GetHistoryLines/GetHistoryAttributes.
type HistoryLine struct {
	Chars      []rune
	Attrs      []Attributes
	CellWidths []int
}

// NewHistoryScreen creates a screen with scrollback.
func NewHistoryScreen(columns, lines, maxHistory int) *HistoryScreen {
	h := &HistoryScreen{
		NativeScreen: *NewNativeScreen(columns, lines),
		store:        NewRowStore(columns, lines, maxHistory),
	}
	h.syncWindow()
	return h
}

// Store exposes the backing store. Phase 3 uses this to drive the display
// cache off store.Gen() instead of manual invalidation.
func (h *HistoryScreen) Store() *RowStore { return h.store }

// syncWindow repoints the embedded screen's cell slices at the rows the
// window currently shows. Called after anything that moves base, viewPos, or
// the geometry.
func (h *HistoryScreen) syncWindow() {
	top := h.store.Base() - h.viewPos
	if top < h.store.Origin() {
		top = h.store.Origin()
	}
	n := h.store.Lines()
	if cap(h.buffer) < n {
		h.buffer = make([][]rune, n)
	}
	if cap(h.attrs) < n {
		h.attrs = make([][]Attributes, n)
	}
	if cap(h.cellWidths) < n {
		h.cellWidths = make([][]int, n)
	}
	h.buffer = h.buffer[:n]
	h.attrs = h.attrs[:n]
	h.cellWidths = h.cellWidths[:n]

	for y := 0; y < n; y++ {
		r := h.store.At(top + y)
		if r == nil {
			// Off the end of the store: back the line with a scratch row so
			// callers never see a nil slice.
			blank := NewRow(h.store.Cols())
			h.buffer[y] = blank.Chars
			h.attrs[y] = blank.Attrs
			h.cellWidths[y] = blank.Widths
			continue
		}
		h.buffer[y] = r.Chars
		h.attrs[y] = r.Attrs
		h.cellWidths[y] = r.Widths
	}
	h.columns = h.store.Cols()
	h.lines = n
}

// snapToLive drops out of scrollback. Unlike the old restoreCurrentScreen
// this cannot lose or duplicate anything: it only zeroes a read offset.
func (h *HistoryScreen) snapToLive() {
	if !h.viewing {
		return
	}
	h.viewPos = 0
	h.viewing = false
	h.cursor.Hidden = false
	h.syncWindow()
	h.store.Touch()
}

// ---- output path ----

// Linefeed moves down a line, pushing to scrollback when at the bottom.
func (h *HistoryScreen) Linefeed() {
	h.snapToLive()

	effectiveLines := h.store.Lines()

	if h.scrollRegionSet {
		bottom := h.scrollBottom
		if bottom >= effectiveLines {
			bottom = effectiveLines - 1
		}
		if h.cursor.Y >= bottom {
			if h.scrollTop == 0 {
				// Region includes the top of the screen, so the displaced
				// line is real scrollback.
				h.store.Advance()
				h.syncWindow()
				if bottom < effectiveLines-1 {
					// Advance moved EVERY row up by one, including the rows
					// below the region, which must not have moved. Shifting
					// [bottom, lines-1] back down restores them AND leaves the
					// region's own bottom row blank, which is what a scroll
					// owes it.
					//
					// This used to say bottom+1, which is off by one: the row
					// that had been at bottom+1 is sitting at bottom after the
					// Advance, so a shift starting at bottom+1 leaves it there.
					// For vim's usual region (everything except the status
					// line) that made it a ONE-row shift of the last row, which
					// the store then refused outright - so the status line was
					// dragged up into the text and stayed there.
					h.store.ScrollRegionDown(bottom, effectiveLines-1)
					h.syncWindow()
				}
			} else {
				h.store.ScrollRegion(h.scrollTop, bottom)
				h.syncWindow()
			}
			h.cursor.Y = bottom
		} else {
			h.cursor.Y++
		}
	} else {
		if h.cursor.Y >= effectiveLines-1 {
			h.store.Advance()
			h.syncWindow()
			h.cursor.Y = effectiveLines - 1
		} else {
			h.cursor.Y++
		}
	}

	if h.newlineMode {
		h.cursor.X = 0
	}
	h.store.Touch()
}

// scrollBounds returns the active scroll region as an inclusive [top,bottom],
// defaulting to the whole screen when no DECSTBM region is set. A region that
// no longer fits the current geometry (after a resize) degrades to the whole
// screen rather than to something out of bounds.
func (h *HistoryScreen) scrollBounds() (int, int) {
	lines := h.store.Lines()
	if !h.scrollRegionSet {
		return 0, lines - 1
	}
	top, bottom := h.scrollTop, h.scrollBottom
	if top < 0 {
		top = 0
	}
	if bottom >= lines {
		bottom = lines - 1
	}
	if top > bottom {
		return 0, lines - 1
	}
	return top, bottom
}

// ReverseIndex moves the cursor up one line, scrolling the region DOWN when it
// is already at the region top.
//
// NativeScreen has a region-aware ReverseIndex, but it operates on h.buffer,
// which under the row store is only an ALIAS of the store's rows - reassigning
// its slice headers changes nothing the renderer will ever read, and the next
// syncWindow overwrites it. The same is true of InsertLines and DeleteLines
// below. All three have to be re-expressed against the store.
func (h *HistoryScreen) ReverseIndex() {
	h.snapToLive()
	top, bottom := h.scrollBounds()
	if h.cursor.Y <= top {
		h.store.ScrollRegionDown(top, bottom)
		h.syncWindow()
		h.cursor.Y = top
	} else {
		h.cursor.Y--
	}
	h.store.Touch()
}

// InsertLines opens count blank lines at the cursor, pushing the rest of the
// region down. Lines pushed past the region bottom are lost - they are NOT
// scrollback, because the region bottom is not the bottom of the screen.
func (h *HistoryScreen) InsertLines(count int) {
	h.snapToLive()
	top, bottom := h.scrollBounds()
	if h.cursor.Y < top || h.cursor.Y > bottom {
		return // the cursor is outside the region; DECSTBM says do nothing
	}
	if count < 1 {
		count = 1
	}
	if n := bottom - h.cursor.Y + 1; count > n {
		count = n
	}
	for i := 0; i < count; i++ {
		h.store.ScrollRegionDown(h.cursor.Y, bottom)
	}
	h.syncWindow()
	h.cursor.X = 0
	h.store.Touch()
}

// DeleteLines removes count lines at the cursor, pulling the rest of the
// region up and blanking the region bottom. The removed lines are NOT pushed
// to scrollback: DL is an edit inside the region, not a scroll of the screen.
func (h *HistoryScreen) DeleteLines(count int) {
	h.snapToLive()
	top, bottom := h.scrollBounds()
	if h.cursor.Y < top || h.cursor.Y > bottom {
		return
	}
	if count < 1 {
		count = 1
	}
	if n := bottom - h.cursor.Y + 1; count > n {
		count = n
	}
	for i := 0; i < count; i++ {
		h.store.ScrollRegion(h.cursor.Y, bottom)
	}
	h.syncWindow()
	h.cursor.X = 0
	h.store.Touch()
}

// Index is Linefeed without the newline-mode carriage return.
func (h *HistoryScreen) Index() {
	saved := h.newlineMode
	h.newlineMode = false
	h.Linefeed()
	h.newlineMode = saved
}

// Draw writes text at the cursor, leaving scrollback first if needed.
func (h *HistoryScreen) Draw(text string) {
	h.snapToLive()
	h.NativeScreen.Draw(text)
	h.store.Touch()
}

// EraseInDisplay clears part or all of the screen.
func (h *HistoryScreen) EraseInDisplay(how int) {
	h.snapToLive()
	h.NativeScreen.EraseInDisplay(how)
	h.store.Touch()
}

// Reset clears the screen and all scrollback.
func (h *HistoryScreen) Reset() {
	h.viewPos = 0
	h.viewing = false
	h.store.Reset()
	h.NativeScreen.Reset()
	h.syncWindow()
}

// Resize changes geometry, preserving scrollback.
func (h *HistoryScreen) Resize(newCols, newLines int) {
	h.snapToLive()

	// The store anchors the BOTTOM of the live window across a line-count
	// change, so base moves and every resident row lands on a different
	// screen row. cursor.Y is a screen row, so it has to move with them or
	// it silently comes to mean a different line of text -- and the next
	// thing written (a shell's prompt) is painted over content that is
	// still on screen, at exactly the row offset the window grew by.
	//
	// The delta is measured from the store rather than derived from
	// newLines-h.lines because base clamps at origin when there is not
	// enough scrollback to expand into.
	oldBase := h.store.Base()
	h.store.Resize(newCols, newLines)
	h.cursor.Y += oldBase - h.store.Base()
	if h.cursor.Y < 0 {
		h.cursor.Y = 0
	}

	h.columns = newCols
	h.lines = newLines
	if h.cursor.Y >= newLines {
		h.cursor.Y = newLines - 1
	}
	if h.cursor.X >= newCols {
		h.cursor.X = newCols - 1
	}
	if !h.scrollRegionSet {
		h.scrollBottom = newLines - 1
	}
	h.syncWindow()
}

// ---- scrollback navigation ----

// ScrollUp moves the view up into scrollback.
//
// Divergence from the old implementation: when there is no scrollback this
// is a no-op. The old code set ViewingHistory=true with pos=0 regardless,
// leaving IsViewingHistory reporting true while the live screen was showing
// -- which the cli layer reads to suppress the cursor and gate auto-scroll.
func (h *HistoryScreen) ScrollUp(lines int) {
	max := h.store.Scrollback()
	if max <= 0 {
		return
	}
	newPos := h.viewPos + lines
	if newPos > max {
		newPos = max
	}
	if newPos <= h.viewPos {
		return
	}
	h.viewPos = newPos
	h.viewing = true
	h.cursor.Hidden = true
	h.syncWindow()
	h.store.Touch()
}

// ScrollDown moves the view back toward live output.
func (h *HistoryScreen) ScrollDown(lines int) {
	if !h.viewing {
		return
	}
	newPos := h.viewPos - lines
	if newPos <= 0 {
		h.snapToLive()
		return
	}
	h.viewPos = newPos
	h.syncWindow()
	h.store.Touch()
}

// ScrollToTop jumps to the oldest retained row.
func (h *HistoryScreen) ScrollToTop() {
	max := h.store.Scrollback()
	if max <= 0 {
		return
	}
	h.viewPos = max
	h.viewing = true
	h.cursor.Hidden = true
	h.syncWindow()
	h.store.Touch()
}

// ScrollToBottom returns to live output.
func (h *HistoryScreen) ScrollToBottom() { h.snapToLive() }

// ---- position reporting ----

func (h *HistoryScreen) GetHistoryPos() int    { return h.viewPos }
func (h *HistoryScreen) GetMaxHistoryPos() int { return h.store.Scrollback() }
func (h *HistoryScreen) GetHistorySize() int   { return h.store.Scrollback() }
func (h *HistoryScreen) IsViewingHistory() bool {
	return h.viewing
}

func (h *HistoryScreen) IsAtTopOfHistory() bool {
	if !h.viewing {
		return false
	}
	return h.viewPos >= h.store.Scrollback()
}

func (h *HistoryScreen) IsAtBottomOfHistory() bool {
	return !h.viewing || h.viewPos <= 0
}

func (h *HistoryScreen) GetScrollProgress() float32 {
	max := h.store.Scrollback()
	if !h.viewing || max <= 0 {
		return 0.0
	}
	p := float32(h.viewPos) / float32(max)
	if p > 1.0 {
		p = 1.0
	}
	if p < 0.0 {
		p = 0.0
	}
	return p
}

// ---- scrollback readers ----

// GetHistoryLines returns the scrollback rows as strings, oldest first.
func (h *HistoryScreen) GetHistoryLines() []string {
	rows := h.store.Range(h.store.Origin(), h.store.Base())
	out := make([]string, 0, len(rows))
	for i := range rows {
		line := renderRow(rows[i].Chars, rows[i].Widths)
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}

// GetHistoryAttributes returns attributes parallel to GetHistoryLines.
func (h *HistoryScreen) GetHistoryAttributes() [][]Attributes {
	rows := h.store.Range(h.store.Origin(), h.store.Base())
	out := make([][]Attributes, 0, len(rows))
	for i := range rows {
		out = append(out, extractRowAttributes(rows[i].Attrs, rows[i].Widths))
	}
	return out
}

// GetDisplay returns the visible window as strings.
func (h *HistoryScreen) GetDisplay() []string {
	out := make([]string, h.lines)
	for y := 0; y < h.lines; y++ {
		out[y] = renderRow(h.buffer[y], h.cellWidths[y])
	}
	return out
}

func (h *HistoryScreen) GetCursor() (int, int) { return h.cursor.X, h.cursor.Y }

func (h *HistoryScreen) GetCursorObject() *Cursor { return &h.cursor }

// ContinuationRune occupies the second column of a double-width glyph in a
// rendered display row.
//
// Rows produced by the display API are column-indexed: exactly one rune per
// terminal column, so a caller may use rune index as column index. That
// invariant is what the renderer, the background layer and the selection
// hit-test all depend on. Dropping the continuation cell instead -- as this
// code did previously -- shortens the row by one rune per wide glyph and
// shifts everything to its right one column left.
//
// Text consumers (copy, search, logging) want the collapsed form and must
// strip the sentinel; see StripContinuations.
const ContinuationRune = '\x00'

// renderRow flattens a row of cells into a column-indexed string. Every
// column contributes exactly one rune: the glyph itself, ContinuationRune
// for the trailing half of a wide glyph, or a space for an empty cell.
func renderRow(chars []rune, widths []int) string {
	if len(chars) == 0 {
		return ""
	}
	out := make([]rune, len(chars))
	for i, ch := range chars {
		switch {
		case i < len(widths) && widths[i] == 0:
			out[i] = ContinuationRune
		case ch == 0:
			// A null in a cell that is not a continuation is an
			// uninitialized cell, not a spacer. Render it blank rather
			// than dropping it, which would shorten the row.
			out[i] = ' '
		default:
			out[i] = ch
		}
	}
	return string(out)
}

// extractRowAttributes returns attributes parallel to renderRow's output:
// one entry per column, continuation cells included. The widths argument is
// retained for symmetry with renderRow and because callers pass the pair
// together; attributes are stored per cell and need no width filtering.
func extractRowAttributes(attrs []Attributes, widths []int) []Attributes {
	out := make([]Attributes, len(attrs))
	copy(out, attrs)
	return out
}

// StripContinuations removes continuation spacers from a rendered row,
// yielding the text a user should see, copy or search.
func StripContinuations(s string) string {
	if !strings.ContainsRune(s, ContinuationRune) {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r != ContinuationRune {
			out = append(out, r)
		}
	}
	return string(out)
}

// StripContinuationLines applies StripContinuations to every row, returning
// a new slice and leaving the input untouched.
func StripContinuationLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = StripContinuations(l)
	}
	return out
}

// SwapStore replaces the backing store and returns the previous one. This is
// how the alternate screen is entered and left: a pointer swap, instead of
// copying three parallel grids in and out and hoping the width grid stays in
// step with the character grid.
func (h *HistoryScreen) SwapStore(s *RowStore) *RowStore {
	prev := h.store
	h.viewPos = 0
	h.viewing = false
	h.store = s
	h.syncWindow()
	return prev
}

// SetMaxHistory changes the scrollback retention budget.
func (h *HistoryScreen) SetMaxHistory(max int) { h.store.SetMaxScrollback(max) }

// SyncWindow re-points the visible cell slices at the store. Exported so
// WideCharScreen can call it after driving the store directly.
//
// INVARIANT: every path that reorders rows in the store (Advance,
// ScrollRegion, ScrollRegionDown) must call this afterwards. h.buffer,
// h.attrs and h.cellWidths are ALIASES of the store's row slices and the
// render path reads the ALIASES, not the store. Reordering rows without
// re-syncing leaves the renderer showing the previous arrangement, which on
// screen is indistinguishable from a scroll that never happened.
func (h *HistoryScreen) SyncWindow() { h.syncWindow() }

// Advance pushes the top screen line into scrollback and opens a blank line
// at the bottom. Both scroll paths -- newline and autowrap -- go through
// this, so they can no longer compute the push differently.
func (h *HistoryScreen) Advance() {
	h.store.Advance()
	h.syncWindow()
}
