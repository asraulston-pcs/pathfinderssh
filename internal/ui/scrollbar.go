// internal/ui/scrollbar.go
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

const (
	vScrollbarWidth    = float32(14) // gutter width reserved on the terminal's right edge
	vScrollbarMinThumb = float32(28) // smallest the thumb is allowed to shrink to
)

// VirtualScrollbar is a thin draggable scrollbar for the terminal's virtual
// scrollback. It scrolls no Fyne content itself - the terminal grid is only
// viewport-sized - it merely reflects the virtual scroll position and calls
// back into the terminal to move the gopyte history view.
//
// Coordinate convention: pos is the thumb's position from the TOP of the track,
// where 0 = top/oldest line and 1 = bottom/newest (the live tail). thumbFrac is
// the fraction of total content currently visible, which sets the thumb height.
type VirtualScrollbar struct {
	widget.BaseWidget

	pos       float32 // 0 = top/oldest, 1 = bottom/newest
	thumbFrac float32 // 0..1, fraction of content visible
	active    bool    // false in alternate screen or when nothing is scrollable

	onScrub func(fraction float32) // invoked with a target pos (0=top, 1=bottom)
}

// NewVirtualScrollbar builds a scrollbar that calls onScrub(fraction) whenever
// the user drags the thumb or clicks the track.
func NewVirtualScrollbar(onScrub func(float32)) *VirtualScrollbar {
	s := &VirtualScrollbar{pos: 1, thumbFrac: 1, active: false, onScrub: onScrub}
	s.ExtendBaseWidget(s)
	return s
}

// SetState updates the displayed position/size/visibility. It is cheap to call
// every frame: it only triggers a refresh when something actually changed.
func (s *VirtualScrollbar) SetState(pos, thumbFrac float32, active bool) {
	pos = clamp01(pos)
	thumbFrac = clamp01(thumbFrac)
	if pos == s.pos && thumbFrac == s.thumbFrac && active == s.active {
		return
	}
	s.pos = pos
	s.thumbFrac = thumbFrac
	s.active = active
	s.Refresh()
}

func (s *VirtualScrollbar) thumbHeight(trackH float32) float32 {
	th := trackH * s.thumbFrac
	if th < vScrollbarMinThumb {
		th = vScrollbarMinThumb
	}
	if th > trackH {
		th = trackH
	}
	return th
}

// scrubToY maps a pointer Y within the track to a target fraction (placing the
// thumb centre under the pointer) and fires the callback.
func (s *VirtualScrollbar) scrubToY(y float32) {
	if !s.active || s.onScrub == nil {
		return
	}
	h := s.Size().Height
	thumbH := s.thumbHeight(h)
	avail := h - thumbH
	if avail <= 0 {
		return
	}
	s.onScrub(clamp01((y - thumbH/2) / avail))
}

// fyne.Draggable - drag the thumb.
func (s *VirtualScrollbar) Dragged(e *fyne.DragEvent) { s.scrubToY(e.Position.Y) }
func (s *VirtualScrollbar) DragEnd()                  {}

// fyne.Tappable - click the track to jump.
func (s *VirtualScrollbar) Tapped(e *fyne.PointEvent) { s.scrubToY(e.Position.Y) }

func (s *VirtualScrollbar) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewRectangle(scrollbarTrackColor())
	thumb := canvas.NewRectangle(scrollbarThumbColor())
	// No CornerRadius: a rounded canvas.Rectangle is drawn via a cached raster
	// that does not always regenerate on a Refresh-time resize. A plain rectangle
	// is drawn as a simple quad at its current size every frame, so the thumb's
	// height tracks the scrollback without stale-raster artifacts.
	return &vScrollbarRenderer{
		s:       s,
		track:   track,
		thumb:   thumb,
		objects: []fyne.CanvasObject{track, thumb},
	}
}

type vScrollbarRenderer struct {
	s       *VirtualScrollbar
	track   *canvas.Rectangle
	thumb   *canvas.Rectangle
	objects []fyne.CanvasObject
}

func (r *vScrollbarRenderer) Destroy()                     {}
func (r *vScrollbarRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *vScrollbarRenderer) MinSize() fyne.Size {
	return fyne.NewSize(vScrollbarWidth, vScrollbarMinThumb*2)
}

func (r *vScrollbarRenderer) Layout(size fyne.Size) {
	r.track.Resize(size)
	r.track.Move(fyne.NewPos(0, 0))
	r.layoutThumb(size)
}

func (r *vScrollbarRenderer) layoutThumb(size fyne.Size) {
	// Always give the thumb real, non-zero geometry. Fyne's painter skips
	// zero-area objects and prunes Hide()-den ones from the scene graph; in either
	// case a later resize issued from Refresh() (which is what SetState triggers,
	// not a Layout pass) fails to re-engage the object, so the thumb would compute
	// correct geometry yet never paint. Keeping it permanently sized is what makes
	// it appear. When nothing is scrollable thumbFrac is 1, so the thumb fills the
	// track ("full bar, nothing to scroll"); as scrollback grows it shrinks.
	if size.Height <= 0 {
		return // transient pre-layout; the first real Layout sizes it
	}
	thumbH := r.s.thumbHeight(size.Height)
	y := (size.Height - thumbH) * r.s.pos
	if y < 0 {
		y = 0
	}
	// Inset the thumb a couple of px horizontally so it reads as a pill, not a
	// full-width block.
	inset := float32(2)
	w := size.Width - inset*2
	if w < 1 {
		w = size.Width
	}
	r.thumb.Resize(fyne.NewSize(w, thumbH))
	r.thumb.Move(fyne.NewPos(inset, y))
	r.thumb.Show()
	canvas.Refresh(r.thumb)
}

func (r *vScrollbarRenderer) Refresh() {
	r.track.FillColor = scrollbarTrackColor()
	r.thumb.FillColor = scrollbarThumbColor()
	r.layoutThumb(r.s.Size())
	canvas.Refresh(r.s)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Neutral semi-transparent colors so the bar reads on both light and dark
// terminal themes without needing theme plumbing. The thumb is the prominent
// element; the track is a faint gutter.
func scrollbarTrackColor() color.Color { return color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0x24} }
func scrollbarThumbColor() color.Color { return color.NRGBA{R: 0x9a, G: 0x9a, B: 0x9a, A: 0xC0} }
