// internal/ui/redraw.go
//
// The coalescing redraw loop, extracted.
//
// Every applet in this package grew the same thing independently: a producer
// on some other goroutine sets a dirty flag, a ticker wakes on an interval and
// does the toolkit work on the main thread through fyne.Do, and a Start gate
// keeps the ticker from firing before the driver exists. Written three times
// it is three chances to get the gate wrong, and getting the gate wrong is a
// nil dereference deep inside a renderer that names a layout function rather
// than the cause.
//
// # Why the gate exists at all
//
// A view is constructed before ShowAndRun and may mark itself dirty
// immediately. fyne.Do needs a running driver. Between construction and
// ShowAndRun there is a window of milliseconds in which a tick would paint
// into nothing, and it is a window that widens on a slow machine, which is how
// it becomes an intermittent crash rather than an obvious one.
//
// # Scope, deliberately
//
// crawlview, captureview and the terminal are NOT migrated onto this. They
// work, they are tested, and moving three known-good views inside a change
// that also adds a new one means the next regression in any of them gets
// blamed on the new feature. This is used by searchview; migrating the others
// is its own change with its own test run.
package ui

import (
	"sync"
	"sync/atomic"
	"time"
)

// redrawTickInterval is what the existing views settled on independently. Fast
// enough that a table does not feel laggy, slow enough that a producer
// emitting from every worker cannot drive the paint rate.
//
// crawlview declares its own redrawInterval at the same value. The two collapse
// into this one when crawlview migrates; until then a shared const would make
// this file depend on a view it is meant to be independent of.
const redrawTickInterval = 200 * time.Millisecond

// redraw coalesces repaint requests from any goroutine into one paint per
// tick on the main thread.
//
// The zero value is not usable; use newRedraw.
type redraw struct {
	interval time.Duration
	paint    func()

	// first runs once, on the first tick rather than in Start. Anything a
	// view wants to do "at startup" that ends in fyne.Do belongs here: at
	// Start time the driver may not be running yet, and one interval later
	// it certainly is. The capture view learned this by racing its first
	// store read against the driver.
	first func()

	started  chan struct{}
	startOne sync.Once
	stopOne  sync.Once
	stopping atomic.Bool
	dirty    atomic.Bool
}

func newRedraw(paint func()) *redraw {
	return &redraw{
		interval: redrawTickInterval,
		paint:    paint,
		started:  make(chan struct{}),
	}
}

// onFirstTick registers work to run once, after the driver is certainly
// running. It must be called before Start.
func (r *redraw) onFirstTick(f func()) { r.first = f }

// mark requests a repaint. Safe from any goroutine, and cheap enough to call
// per event: a thousand marks between two ticks cost one paint.
func (r *redraw) mark() { r.dirty.Store(true) }

// start releases the loop. Call it immediately before ShowAndRun, or from the
// applet's Start, which the shell calls at the same point.
func (r *redraw) start() {
	r.startOne.Do(func() { close(r.started) })
}

// stop ends the loop. It is idempotent because a view can be closed by the tab
// button, by the window close box, or by its own producer finishing, and all
// three reach it.
func (r *redraw) stop() {
	r.stopOne.Do(func() { r.stopping.Store(true) })
}

// run is the loop itself. Launch it as a goroutine at construction; it blocks
// until start.
func (r *redraw) run() {
	select {
	case <-r.started:
	case <-time.After(30 * time.Second):
		// A view constructed and never started is a bug in the host,
		// not a reason to leak a goroutine for the life of the
		// process.
		return
	}

	t := time.NewTicker(r.interval)
	defer t.Stop()

	first := true
	for range t.C {
		if r.stopping.Load() {
			return
		}
		if first {
			first = false
			if r.first != nil {
				r.first()
			}
		}
		// Swap rather than Load-then-Store: a mark arriving between
		// the two would otherwise be dropped, and a dropped mark is a
		// view that stops updating until the next unrelated event.
		if !r.dirty.Swap(false) {
			continue
		}
		r.paint()
	}
}
