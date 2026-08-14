// internal/ui/shellmodel.go
//
// The shell's bookkeeping, with no toolkit in it.
//
// What lives here is the part that has answers a test can check: how many
// instances are open, what each one is called, whether it is in a tab or in its
// own window. Splitting it out follows the same rule as crawlrun and
// capturerun -- the model is testable without a display, and the widget layer
// is left holding only the things that genuinely need one.
//
// The lock is here for a reason worth stating. Every mutation is supposed to
// happen on the UI goroutine, because every mutation is paired with a container
// change and Fyne is single-threaded. But a session's transport closes on its
// own goroutine and a crawl finishes on another, and either of those wanting to
// know what is still open is a reasonable thing that should not have to hop
// threads first. Reads are therefore safe from anywhere; writes are still the
// UI goroutine's job.
package ui

import (
	"fmt"
	"sync"
)

// Kind names an applet type. It exists so the shell can count, label and
// arrange instances without switching on a Go type.
type Kind string

const (
	KindTerminal Kind = "terminal"
	KindCrawl    Kind = "crawl"
	KindCapture  Kind = "capture"
	KindSearch   Kind = "search"
)

// Placement is where an instance is currently displayed.
type Placement int

const (
	// Docked means the instance is a tab in the main window.
	Docked Placement = iota

	// Detached means the instance has a window of its own. The instance is
	// unchanged by the move: the same object is displayed somewhere else.
	Detached
)

func (p Placement) String() string {
	if p == Detached {
		return "detached"
	}
	return "docked"
}

// InstanceInfo is what the shell knows about one applet instance.
type InstanceInfo struct {
	ID   int
	Kind Kind

	// Base is the title as requested; Title is Base made unique. Two tabs
	// reading "crawl" is a UI that cannot tell you which crawl you are
	// looking at, and the crawl you want to stop is usually not the one you
	// are looking at.
	Base  string
	Title string

	Placement Placement
}

// Registry tracks open instances in display order.
type Registry struct {
	mu    sync.RWMutex
	next  int
	items []*InstanceInfo
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Add records a new instance and returns it. The returned pointer is the
// registry's own: the caller may read it, and the shell updates Placement
// through the registry rather than by writing to it directly.
func (r *Registry) Add(kind Kind, base string) *InstanceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()

	if base == "" {
		base = string(kind)
	}
	r.next++
	info := &InstanceInfo{
		ID:        r.next,
		Kind:      kind,
		Base:      base,
		Title:     r.uniqueTitleLocked(base),
		Placement: Docked,
	}
	r.items = append(r.items, info)
	return info
}

// uniqueTitleLocked suffixes a title until nothing else is using it. The suffix
// counts collisions rather than instances, so closing "crawl" and opening
// another does not leave a gap that reads like a missing tab.
func (r *Registry) uniqueTitleLocked(base string) string {
	taken := make(map[string]bool, len(r.items))
	for _, it := range r.items {
		taken[it.Title] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)", base, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

// Remove drops an instance. It reports whether anything was removed, so a
// double close is detectable rather than silent.
func (r *Registry) Remove(id int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, it := range r.items {
		if it.ID == id {
			r.items = append(r.items[:i], r.items[i+1:]...)
			return true
		}
	}
	return false
}

// SetPlacement records a move between a tab and a window.
func (r *Registry) SetPlacement(id int, p Placement) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, it := range r.items {
		if it.ID == id {
			it.Placement = p
			return
		}
	}
}

// Get returns one instance's info, or nil.
func (r *Registry) Get(id int) *InstanceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, it := range r.items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// All returns a snapshot in display order.
func (r *Registry) All() []*InstanceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*InstanceInfo, len(r.items))
	copy(out, r.items)
	return out
}

// Len is the number of open instances.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

// CountKind is how many instances of one kind are open.
func (r *Registry) CountKind(k Kind) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, it := range r.items {
		if it.Kind == k {
			n++
		}
	}
	return n
}

// CountPlacement is how many instances are docked or detached.
func (r *Registry) CountPlacement(p Placement) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, it := range r.items {
		if it.Placement == p {
			n++
		}
	}
	return n
}

// Summary is the one-line description of what is open, for the toolbar. It
// says nothing when nothing is open, because the toolbar already says what to
// do about that.
func (r *Registry) Summary() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.items) == 0 {
		return ""
	}
	counts := map[Kind]int{}
	for _, it := range r.items {
		counts[it.Kind]++
	}
	var parts []string
	// Fixed order rather than map order: the summary line is read at a
	// glance and a line whose parts reshuffle between renders is read
	// twice every time. A new Kind that is not added here counts for
	// nothing and shows nothing.
	for _, k := range []Kind{KindTerminal, KindCrawl, KindCapture, KindSearch} {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(string(k), n)))
		}
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	if n := r.countPlacementLocked(Detached); n > 0 {
		out += fmt.Sprintf(" · %d in own window", n)
	}
	return out
}

func (r *Registry) countPlacementLocked(p Placement) int {
	n := 0
	for _, it := range r.items {
		if it.Placement == p {
			n++
		}
	}
	return n
}

func plural(s string, n int) string {
	if n == 1 {
		return s
	}
	return s + "s"
}
