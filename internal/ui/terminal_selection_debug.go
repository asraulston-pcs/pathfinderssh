// internal/ui/terminal_selection_debug.go
//
// Temporary instrumentation for the "selection lands one row above the pointer"
// bug. Drop this file in, call DumpSelectionGeometry from HandleMouseDown (see
// the comment on the function), reproduce, and read the log.
//
// Everything here is read-only with one exception, called out below.
package ui

import (
	"fmt"
	"log"
	"strings"

	"fyne.io/fyne/v2"
)

// DumpSelectionGeometry logs every quantity that sits between "pixel the user
// clicked" and "absolute buffer line the selection anchors to", so the next
// occurrence identifies which stage is wrong instead of which stage is
// suspected.
//
// Wire it up as the first line of SelectionManager.HandleMouseDown:
//
//	func (sm *SelectionManager) HandleMouseDown(event *desktop.MouseEvent) bool {
//	        if event.Button != desktop.MouseButtonPrimary {
//	                return false
//	        }
//	        DumpSelectionGeometry(sm.terminal, event.Position, "mousedown")
//	        ...
//
// and (with a *fyne.PointEvent) at the top of DoubleTapped / TripleTapped, so
// the three entry points can be compared against each other in one log.
//
// NOTE: this calls GetDisplay(), which is NOT a pure getter - it recomputes and
// mutates the screen's viewportStart/viewportEnd. That is the same thing the
// live selection path already does, so the instrumentation does not change
// behaviour, but do not "optimize" it into the render loop.
func DumpSelectionGeometry(t *NativeTerminalWidget, pos fyne.Position, source string) {
	if t == nil || t.textGrid == nil || t.screen == nil {
		log.Printf("[selgeo/%s] widget not ready", source)
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[selgeo/%s] pos=(%.1f,%.1f)\n", source, pos.X, pos.Y)

	// --- Stage 1: cell metrics -------------------------------------------
	// The grid's OWN cell size, read back through an exported API rather than
	// recomputed: PositionForCursorLocation(1,0).Y is exactly cellSize.Height,
	// and (0,1).X is cellSize.Width. This is the divisor
	// CursorLocationForPosition uses, so it is the only metric that can be
	// called correct; gridCellSize() and charHeight are predictions of it.
	trueCell := t.textGrid.PositionForCursorLocation(1, 1)
	measCW, measCH := t.gridCellSize()
	fmt.Fprintf(&b, "  cell: grid=(%.2f,%.2f) gridCellSize=(%.2f,%.2f) charW/H=(%.2f,%.2f) fontSize=%.1f\n",
		trueCell.X, trueCell.Y, measCW, measCH, t.charWidth, t.charHeight, t.fontSize)
	if trueCell.Y > 0 {
		// How far down the screen before a 1px-per-row error becomes a whole row.
		if d := measCH - trueCell.Y; d != 0 {
			fmt.Fprintf(&b, "  cell: MISMATCH dh=%.3f -> full row of drift by row %.1f\n",
				d, float64(trueCell.Y)/absf(float64(d)))
		}
	}

	// --- Stage 2: coordinate space ---------------------------------------
	// gridOrigin is measured relative to the TERMINAL widget. Mouse
	// down/up/drag are delivered to HybridScrollContainer, so their Position
	// is relative to the SCROLL container. Double/triple tap land on the
	// terminal itself. If those two origins differ, one of the three entry
	// points is subtracting an offset the other should not.
	origin, okOrigin := t.gridOrigin()
	findVisible := t.find != nil && t.find.bar != nil && t.find.bar.Visible()
	fmt.Fprintf(&b, "  origin: grid-vs-terminal=(%.1f,%.1f) ok=%v findbar_visible=%v\n",
		origin.X, origin.Y, okOrigin, findVisible)

	rowRaw, colRaw := t.textGrid.CursorLocationForPosition(pos)
	rowAdj, colAdj := t.textGrid.CursorLocationForPosition(pos.Subtract(origin))
	fmt.Fprintf(&b, "  hittest: raw=(r%d,c%d) origin-adjusted=(r%d,c%d) delta_rows=%d\n",
		rowRaw, colRaw, rowAdj, colAdj, rowRaw-rowAdj)

	// --- Stage 3: live buffer state --------------------------------------
	allLines := t.screen.GetDisplay()
	vp := t.calculateUnifiedViewport(allLines)
	vpStart := t.screen.GetViewportStart()
	total := t.screen.GetTotalContentLines()
	topAbs := vpStart + vp.scrollOffset
	fmt.Fprintf(&b, "  buffer: display=%d rows=%d visible=%d scrollOffset=%d maxScroll=%d viewportStart=%d totalLines=%d hist=%v alt=%v\n",
		len(allLines), t.rows, vp.visibleLines, vp.scrollOffset, vp.maxScroll,
		vpStart, total, t.screen.IsViewingHistory(), t.screen.IsUsingAlternate())
	fmt.Fprintf(&b, "  topAbs=%d (live)\n", topAbs)

	// --- Stage 4: what the click resolves to, vs its neighbours ----------
	// The pointer row from the authority (origin-adjusted, same as posToAbs),
	// then the text of that absolute line and the two around it. Compare
	// against what is on screen under the cursor: whichever of prev/hit/next
	// the user was pointing at names the sign and size of the error.
	row := rowAdj
	if row < 0 {
		row = 0
	}
	if vp.visibleLines > 0 && row > vp.visibleLines-1 {
		row = vp.visibleLines - 1
	}
	absLine := topAbs + row
	for _, off := range []int{-1, 0, 1} {
		idx := absLine + off
		if idx < 0 || (total > 0 && idx > total-1) {
			continue
		}
		lines := t.screen.GetLinesInRange(idx, idx+1)
		if len(lines) == 0 {
			continue
		}
		tag := "     "
		if off == 0 {
			tag = "  -> "
		}
		fmt.Fprintf(&b, "%sabs[%d] %q\n", tag, idx, trunc(lines[0], 78))
	}

	// Same absolute line, reached through the PAINTED window instead of the
	// absolute one. These two must name the same text. If they diverge, the
	// pixels on screen are from an older frame than the state the click was
	// resolved against, and the bug is a paint/live desync, not geometry.
	if wr := vp.scrollOffset + row; wr >= 0 && wr < len(allLines) {
		fmt.Fprintf(&b, "  window[%d] %q\n", wr, trunc(allLines[wr], 78))
	}

	log.Print(b.String())
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return strings.TrimRight(string(r), " ")
	}
	return strings.TrimRight(string(r[:n]), " ") + "…"
}