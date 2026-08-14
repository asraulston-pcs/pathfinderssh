// internal/ui/terminal_bglayer.go
package ui

import (
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/gopyte"
)

// bgLayer draws cell backgrounds (SGR bg + selection) as coalesced, device-pixel
// snapped rectangles BEHIND a now-transparent TextGrid. It exists because Fyne's
// TextGrid tiles one canvas.Rectangle per cell at a fractional cell width, which
// leaves sub-pixel seams between adjacent backgrounds on HiDPI displays. Drawing
// runs ourselves with snapped edges eliminates the seam by construction and lets
// selection share the exact same path as SGR backgrounds.
//
// Coordinate space is viewport-local (rows 0..visibleLines-1, cols 0..cols-1),
// matching exactly the window the TextGrid is currently showing under virtual
// scrolling. The layer never needs to know about history offset.
type bgLayer struct {
	widget.BaseWidget
	term *NativeTerminalWidget
	runs []bgRun
}

// bgRun is a horizontal run of cells on one row sharing a single background color.
// endCol is exclusive.
type bgRun struct {
	row      int
	startCol int
	endCol   int
	c        color.Color
}

// selRange is a normalized selection in viewport-local cell coordinates.
// endCol is exclusive on endRow.
type selRange struct {
	startRow, startCol int
	endRow, endCol     int
}

func (s *selRange) contains(r, c int) bool {
	if r < s.startRow || r > s.endRow {
		return false
	}
	switch {
	case s.startRow == s.endRow:
		return c >= s.startCol && c < s.endCol
	case r == s.startRow:
		return c >= s.startCol
	case r == s.endRow:
		return c < s.endCol
	default:
		return true
	}
}

func newBGLayer(t *NativeTerminalWidget) *bgLayer {
	l := &bgLayer{term: t}
	l.ExtendBaseWidget(l)
	return l
}

// Update rebuilds the run list from the currently-visible attributes plus the
// optional selection, then repaints. Call this from your render path right after
// you compute visibleAttrs (pass sel == nil when there's no active selection).
func (l *bgLayer) Update(attrs [][]gopyte.Attributes, sel *selRange) {
	runs := l.runs[:0] // reuse backing array across frames

	for r := 0; r < len(attrs); r++ {
		rowAttrs := attrs[r]
		c := 0
		for c < len(rowAttrs) {
			cur := l.cellBG(rowAttrs[c], sel, r, c)
			if cur == nil {
				c++
				continue
			}
			start := c
			c++
			for c < len(rowAttrs) && sameColor(l.cellBG(rowAttrs[c], sel, r, c), cur) {
				c++
			}
			runs = append(runs, bgRun{row: r, startCol: start, endCol: c, c: cur})
		}
	}

	l.runs = runs
	l.Refresh()
}

// cellBG is the single source of truth for "what color is this cell's background."
// Selection wins over SGR bg; Reverse swaps fg/bg. Your TextGrid glyph path should
// converge on this same logic so the two never disagree. Returns nil for "default
// background" (nothing to draw).
func (l *bgLayer) cellBG(a gopyte.Attributes, sel *selRange, r, c int) color.Color {
	if sel != nil && sel.contains(r, c) {
		return selectionColor
	}
	if a.Reverse {
		// Reverse video: the glyph's foreground becomes the cell background.
		if fg := l.term.mapColor(a.Fg); fg != nil {
			return fg
		}
		return defaultReverseBG
	}
	return l.term.mapColor(a.Bg) // nil == default, skipped by caller
}

var (
	selectionColor   = color.RGBA{0x0C, 0x7A, 0xCC, 0xFF} // matches old ApplyHighlight blue
	defaultReverseBG = color.RGBA{0xCC, 0xCC, 0xCC, 0xFF} // fg-on-default reverse fallback
)

// --- renderer ---------------------------------------------------------------

func (l *bgLayer) CreateRenderer() fyne.WidgetRenderer {
	return &bgLayerRenderer{l: l}
}

type bgLayerRenderer struct {
	l     *bgLayer
	rects []*canvas.Rectangle // pooled; grown as needed, hidden when unused
}

func (r *bgLayerRenderer) place() {
	cw, ch := r.l.cellMetric()
	scale := r.l.canvasScale()
	runs := r.l.runs

	for len(r.rects) < len(runs) {
		rect := canvas.NewRectangle(color.Transparent)
		r.rects = append(r.rects, rect)
	}

	for i, run := range runs {
		// Snap every edge to a whole device pixel. A run's right edge and its
		// neighbor's left edge are computed from the same column index, so they
		// snap to the same value -> zero gap, and integer device pixels -> no
		// anti-aliased ghost line.
		xL := snap(float32(run.startCol)*cw, scale)
		xR := snap(float32(run.endCol)*cw, scale)
		yT := snap(float32(run.row)*ch, scale)
		yB := snap(float32(run.row+1)*ch, scale)

		rect := r.rects[i]
		rect.FillColor = run.c
		rect.Move(fyne.NewPos(xL, yT))
		rect.Resize(fyne.NewSize(xR-xL, yB-yT))
		rect.Show()
	}
	for i := len(runs); i < len(r.rects); i++ {
		r.rects[i].Hide()
	}
}

func (r *bgLayerRenderer) Layout(fyne.Size) { r.place() }

func (r *bgLayerRenderer) Refresh() {
	r.place()
	canvas.Refresh(r.l)
}

func (r *bgLayerRenderer) Objects() []fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, len(r.rects))
	for i, rect := range r.rects {
		objs[i] = rect
	}
	return objs
}

func (r *bgLayerRenderer) MinSize() fyne.Size { return fyne.NewSize(0, 0) }
func (r *bgLayerRenderer) Destroy()           {}

// --- helpers -----------------------------------------------------------------

// cellMetric reads TextGrid's ACTUAL cell size via the terminal's gridCellSize,
// the single source of truth shared with mouse hit-testing so the overlay and
// clicks never disagree. See NativeTerminalWidget.gridCellSize.
func (l *bgLayer) cellMetric() (cw, ch float32) {
	return l.term.gridCellSize()
}

func (l *bgLayer) canvasScale() float32 {
	if c := fyne.CurrentApp().Driver().CanvasForObject(l); c != nil {
		if s := c.Scale(); s > 0 {
			return s
		}
	}
	return 1
}

// snap rounds a logical coordinate to a whole device pixel and converts back to
// logical units, so Fyne's rasterizer lands it on an exact pixel boundary.
func snap(x, scale float32) float32 {
	return float32(math.Round(float64(x*scale))) / scale
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
