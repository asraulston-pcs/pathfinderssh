// internal/crawlrun/run.go
//
// The run model: what a crawl looks like as state rather than as output.
//
// This is the whole difference between an application and a script runner. A
// log is only useful while it is scrolling; a run you can still interrogate
// after it finishes — which devices were never dialed, which one needed the
// address fallback, which credential won where — is a different kind of
// object. Everything in this file is deliberately free of any UI dependency
// so that the answer to "what happened" is testable without a toolkit.
//
// Safe for concurrent use: a crawl emits from every worker goroutine while the
// UI reads on the main thread.
package crawlrun

import (
	"sort"
	"sync"
	"time"
)

// State is where a device ended up. The third outcome is the point: a device
// that was deliberately not connected to is neither a success nor a failure,
// and collapsing it into either one is how it becomes invisible.
type State int

const (
	StateQueued State = iota
	StateRunning
	StateReached
	StateFailed
	StateNotDialed
)

func (s State) String() string {
	switch s {
	case StateQueued:
		return "queued"
	case StateRunning:
		return "running"
	case StateReached:
		return "reached"
	case StateFailed:
		return "failed"
	case StateNotDialed:
		return "not dialed"
	}
	return "?"
}

// DeviceRow is one line of the results table.
type DeviceRow struct {
	Identity string
	Name     string
	Depth    int
	Platform string
	State    State

	// Via is the device that reported this one, empty for a seed.
	Via string

	// Detail is why, for the states that have a why.
	Detail string

	Credential string
	CredReason string

	// Attempts is how many credentials were offered before one worked. This
	// is the lockout-exposure number: every attempt past the first is a
	// failed authentication against a real account, and a run whose average
	// climbs is spending them somewhere new.
	Attempts int

	// Neighbors is what the collection commands parsed, and New is how many
	// of those the crawl had not already claimed.
	Neighbors int
	New       int

	FirstSeen time.Time
	Ended     time.Time
}

// Duration is how long the device took, or zero if it has not finished.
func (d DeviceRow) Duration() time.Duration {
	if d.Ended.IsZero() || d.FirstSeen.IsZero() {
		return 0
	}
	return d.Ended.Sub(d.FirstSeen)
}

// Display is the best label available: what the device calls itself once it
// has said, and the string it was claimed under before that.
func (d DeviceRow) Display() string {
	if d.Name != "" {
		return d.Name
	}
	return d.Identity
}

// rowNote is the short per-device annotation for the Detail column: the
// out-of-the-ordinary thing that happened on the way to reaching this device.
// Empty for the routine cases, which is most of them.
func rowNote(ev Event) string {
	switch ev.Kind {
	case KindRetryAddr:
		return "unreachable by name; reached at " + ev.Detail
	case KindResolved:
		return ev.Detail
	case KindPlatform:
		return ev.Detail // "no neighbor plan; leaf", or empty
	}
	return ""
}

// Counts is the header summary.
type Counts struct {
	Queued    int
	Running   int
	Reached   int
	Failed    int
	NotDialed int

	// NewHostKeys is how many devices were trusted on first contact this
	// run. Expected to be large on a first crawl and near zero afterwards;
	// a later run that jumps is worth a look.
	NewHostKeys int

	// Attempts is the total credentials offered across the run, and
	// Rejections is how many of those were refused. Rejections is the number
	// worth watching — it is the run's cost in failed authentications.
	Attempts   int
	Rejections int
}

// Total is every device the crawl knows about.
func (c Counts) Total() int {
	return c.Queued + c.Running + c.Reached + c.Failed + c.NotDialed
}

// AttemptsPerReached is the ladder cost per device that answered. A warm
// binding store holds this near 1.0; a cold or split one pushes it up, and the
// difference is paid in failed authentications.
func (c Counts) AttemptsPerReached() float64 {
	if c.Reached == 0 {
		return 0
	}
	return float64(c.Attempts) / float64(c.Reached)
}

// Run accumulates events into the state a view renders.
type Run struct {
	mu     sync.RWMutex
	rows   map[string]*DeviceRow
	order  []string
	notes  []Event
	depth  int
	begun  time.Time
	closed time.Time

	// maxNotes bounds the decisions list. A crawl that produces more than
	// this has a systemic problem and the first few say so.
	maxNotes int

	// changed is signalled on every mutation so a view can redraw without
	// polling on a timer.
	changed func()
}

// New returns an empty run.
func New() *Run {
	return &Run{rows: map[string]*DeviceRow{}, maxNotes: 500, begun: time.Now()}
}

// OnChange installs a redraw hook. It is called from crawl goroutines, so a
// toolkit view must marshal to its own thread inside the callback.
func (r *Run) OnChange(f func()) {
	r.mu.Lock()
	r.changed = f
	r.mu.Unlock()
}

// Emit returns the hook to hand the crawler.
func (r *Run) Emit() Emit { return func(ev Event) { r.Handle(ev) } }

func (r *Run) rowLocked(id string, at time.Time) *DeviceRow {
	if d, ok := r.rows[id]; ok {
		return d
	}
	d := &DeviceRow{Identity: id, State: StateQueued, FirstSeen: at}
	r.rows[id] = d
	r.order = append(r.order, id)
	return d
}

// Handle folds one event into the run.
func (r *Run) Handle(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}

	r.mu.Lock()
	if ev.Kind == KindDepth {
		r.depth = ev.Depth
	}
	if ev.Identity != "" {
		d := r.rowLocked(ev.Identity, ev.At)
		if ev.Name != "" {
			d.Name = ev.Name
		}
		if ev.Platform != "" {
			d.Platform = ev.Platform
		}
		if ev.Via != "" && d.Via == "" {
			// First reporter wins. A device several neighbors see would
			// otherwise flip between them on every run and turn the
			// comparison tab into noise.
			d.Via = ev.Via
		}

		switch ev.Kind {
		case KindQueued:
			d.Depth = ev.Depth

		case KindNotDialed:
			// Terminal, and deliberately not a failure.
			d.State, d.Detail, d.Ended = StateNotDialed, ev.Detail, ev.At
			// Depth too. A not-dialed device is never queued, so
			// KindQueued never runs for it and nothing else would
			// ever set this -- leaving every excluded device
			// reading as depth 0, which on a run where most
			// devices are excluded makes the column meaningless
			// and the Depth sort useless.
			if ev.Depth > 0 {
				d.Depth = ev.Depth
			}

		case KindResolved, KindRetryAddr, KindRenamed, KindPlatform, KindHostKeyNew:
			if d.State == StateQueued {
				d.State = StateRunning
			}
			// Detail is blank for most rows, because most devices simply
			// answer. When something out of the ordinary happened to THIS
			// device, that belongs on its row and not only in the decisions
			// list — the list says what happened during the run, the row
			// says what happened to the device you are looking at.
			if note := rowNote(ev); note != "" {
				d.Detail = note
			}

		case KindAuthOK:
			d.State = StateRunning
			d.Attempts++
			d.Credential, d.CredReason = ev.Credential, ev.CredReason

		case KindAuthReject:
			d.State = StateRunning
			d.Attempts++

		case KindCollect:
			d.State = StateRunning
			d.Neighbors += ev.Parsed
			d.New += ev.New

		case KindReached:
			d.State, d.Ended = StateReached, ev.At

		case KindFailed:
			d.State, d.Detail, d.Ended = StateFailed, ev.Detail, ev.At
		}
	}

	if ev.Notable() {
		r.notes = append(r.notes, ev)
		if len(r.notes) > r.maxNotes {
			r.notes = r.notes[len(r.notes)-r.maxNotes:]
		}
	}
	changed := r.changed
	r.mu.Unlock()

	if changed != nil {
		changed()
	}
}

// Finish marks the run complete. Anything still running is recorded as failed
// rather than left mid-flight, because a row that stays "running" forever is
// the same silent gap this package exists to close.
func (r *Run) Finish() {
	r.mu.Lock()
	r.closed = time.Now()
	for _, d := range r.rows {
		if d.State == StateQueued || d.State == StateRunning {
			d.State = StateFailed
			if d.Detail == "" {
				d.Detail = "run ended before this device completed"
			}
			d.Ended = r.closed
		}
	}
	changed := r.changed
	r.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// Rows returns a snapshot in discovery order.
func (r *Run) Rows() []DeviceRow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]DeviceRow, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.rows[id])
	}
	return out
}

// RowsByState returns a snapshot filtered to one state — the click-through
// behind each counter.
func (r *Run) RowsByState(s State) []DeviceRow {
	out := make([]DeviceRow, 0, 8)
	for _, d := range r.Rows() {
		if d.State == s {
			out = append(out, d)
		}
	}
	return out
}

// Sorted returns rows ordered by a named column, for table header clicks.
func (r *Run) Sorted(column string, asc bool) []DeviceRow {
	rows := r.Rows()
	less := func(i, j int) bool { return rows[i].Display() < rows[j].Display() }
	switch column {
	case "depth":
		less = func(i, j int) bool { return rows[i].Depth < rows[j].Depth }
	case "state":
		less = func(i, j int) bool { return rows[i].State < rows[j].State }
	case "platform":
		less = func(i, j int) bool { return rows[i].Platform < rows[j].Platform }
	case "attempts":
		less = func(i, j int) bool { return rows[i].Attempts < rows[j].Attempts }
	case "neighbors":
		less = func(i, j int) bool { return rows[i].Neighbors < rows[j].Neighbors }
	case "via":
		less = func(i, j int) bool { return rows[i].Via < rows[j].Via }
	case "duration":
		less = func(i, j int) bool { return rows[i].Duration() < rows[j].Duration() }
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if asc {
			return less(i, j)
		}
		return less(j, i)
	})
	return rows
}

// Counts summarizes the run.
func (r *Run) Counts() Counts {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var c Counts
	for _, d := range r.rows {
		switch d.State {
		case StateQueued:
			c.Queued++
		case StateRunning:
			c.Running++
		case StateReached:
			c.Reached++
		case StateFailed:
			c.Failed++
		case StateNotDialed:
			c.NotDialed++
		}
		c.Attempts += d.Attempts
	}
	for _, ev := range r.notes {
		switch ev.Kind {
		case KindAuthReject:
			c.Rejections++
		case KindHostKeyNew:
			c.NewHostKeys++
		}
	}
	return c
}

// Decisions returns the notable events, most recent last.
func (r *Run) Decisions() []Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Event(nil), r.notes...)
}

// Depth is the deepest batch started so far.
func (r *Run) Depth() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.depth
}

// Elapsed is how long the run has been going, or how long it took.
func (r *Run) Elapsed() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.closed.IsZero() {
		return r.closed.Sub(r.begun)
	}
	return time.Since(r.begun)
}
