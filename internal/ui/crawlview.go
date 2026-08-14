// internal/ui/crawlview.go
//
// The crawl execution view.
//
// A log pane would have been less work and would have made this look like a
// script runner with a window around it. The difference is not decoration: a
// log can only be read while it scrolls, and every question worth asking about
// a crawl — which devices were never dialed, which one needed the address
// fallback, which credential won where — is asked afterwards.
//
// So the primary surface is a table over crawlrun.Run, not a text buffer. The
// log still exists; it moved to the per-device detail, where it is the answer
// to a question rather than the whole interface.
//
// # The counters
//
// Reached / Failed / Not dialed, and the third is the reason this layout
// exists. A device excluded by pattern or sitting outside AllowDomains is
// drawn into the map as a leaf, indistinguishable from a genuine edge device,
// and in a log its only trace is one line that has already gone. As a counter
// you can click, it stops being invisible.
//
// # Redraw coalescing
//
// A crawl emits from every worker goroutine, thousands of events over a run.
// Refreshing per event would spend the whole frame budget on a table nobody
// can read at that rate, so changes set a dirty flag and a ticker redraws at a
// fixed cadence. The last event before completion is always drawn, because a
// view that settles one update short of the truth is worse than a slow one.
package ui

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
)

// redrawInterval is the table's refresh cadence while a crawl is running.
const redrawInterval = 200 * time.Millisecond

var crawlColumns = []struct {
	title string
	key   string
	width float32
}{
	{"Device", "name", 220},
	{"Depth", "depth", 60},
	{"Platform", "platform", 120},
	{"State", "state", 100},
	{"Cred", "cred", 140},
	{"Try", "attempts", 50},
	{"Nbrs", "neighbors", 60},
	{"Via", "via", 150},
	{"Time", "duration", 70},
	{"Detail", "detail", 320},
}

// CrawlView renders a run. Construct it once and hand its Emit to the crawler.
type CrawlView struct {
	run *crawlrun.Run

	// OnConnect is called when a row is activated. This is the loop the
	// whole shell exists to close: a device in a crawl result is a device
	// you can open a session to, without retyping anything.
	OnConnect func(crawlrun.DeviceRow)

	// OnInspect is called for the detail pane — the captured transcript for
	// one device. Crawl output is not an interactive session, so this wants
	// a pager, not a PTY; the terminal widget renders it read-only.
	OnInspect func(crawlrun.DeviceRow)

	mu       sync.RWMutex
	rows     []crawlrun.DeviceRow
	filter   filterState
	sortKey  string
	sortAsc  bool
	changes  []crawlrun.Change
	dirty    atomic.Bool
	stopping atomic.Bool

	started   chan struct{}
	table     *widget.Table
	decisions *widget.List
	summary   *widget.Label
	counters  map[string]*widget.Button
	content   fyne.CanvasObject
}

type filterState struct {
	active bool
	state  crawlrun.State
}

// NewCrawlView builds the view over a run.
func NewCrawlView(run *crawlrun.Run) *CrawlView {
	v := &CrawlView{
		run:      run,
		sortKey:  "depth",
		sortAsc:  true,
		counters: map[string]*widget.Button{},
		started:  make(chan struct{}),
	}
	v.build()

	// The crawler emits from its workers; Fyne is single-threaded. Set the
	// flag here and let the ticker do the toolkit work on the main thread.
	run.OnChange(func() { v.dirty.Store(true) })
	go v.redrawLoop()

	return v
}

// Start releases the redraw loop. Call it immediately before ShowAndRun.
//
// The loop cannot run earlier: it calls fyne.Do, which hands work to the
// driver, and the driver does not exist until the app is running. Without this
// gate a Compare or an early event would schedule a redraw into nothing.
func (v *CrawlView) Start() {
	select {
	case <-v.started:
	default:
		close(v.started)
	}
}

// Content is the object to place in a tab or split.
func (v *CrawlView) Content() fyne.CanvasObject { return v.content }

// Emit is the hook to hand crawler.Config.
func (v *CrawlView) Emit() crawlrun.Emit { return v.run.Emit() }

// Stop ends the redraw loop. Call it when the view is discarded.
func (v *CrawlView) Stop() { v.stopping.Store(true) }

func (v *CrawlView) build() {
	v.summary = widget.NewLabel("")

	counterBar := container.NewHBox()
	for _, c := range []struct {
		key   string
		label string
		state crawlrun.State
	}{
		{"reached", "Reached", crawlrun.StateReached},
		{"failed", "Failed", crawlrun.StateFailed},
		{"notdialed", "Not dialed", crawlrun.StateNotDialed},
	} {
		state := c.state
		key := c.key
		b := widget.NewButton(c.label+" 0", func() { v.toggleFilter(key, state) })
		b.Importance = widget.LowImportance
		v.counters[key] = b
		counterBar.Add(b)
	}
	all := widget.NewButton("All", func() { v.clearFilter() })
	all.Importance = widget.LowImportance
	counterBar.Add(all)
	counterBar.Add(v.summary)

	v.table = widget.NewTable(v.tableSize, v.makeCell, v.updateCell)
	v.table.ShowHeaderRow = true
	v.table.CreateHeader = func() fyne.CanvasObject {
		return widget.NewButton("", nil)
	}
	v.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		b, ok := o.(*widget.Button)
		if !ok || id.Col < 0 || id.Col >= len(crawlColumns) {
			return
		}
		col := crawlColumns[id.Col]
		title := col.title
		if v.sortKey == col.key {
			if v.sortAsc {
				title += " ^"
			} else {
				title += " v"
			}
		}
		b.SetText(title)
		b.OnTapped = func() { v.sortBy(col.key) }
	}
	for i, c := range crawlColumns {
		v.table.SetColumnWidth(i, c.width)
	}
	v.table.OnSelected = func(id widget.TableCellID) {
		row, ok := v.rowAt(id.Row)
		if !ok {
			return
		}
		v.table.UnselectAll()
		// A device that was never connected to has no session to open and no
		// transcript to show; the row's reason is the whole answer.
		if row.State == crawlrun.StateNotDialed {
			return
		}
		if v.OnInspect != nil {
			v.OnInspect(row)
		}
	}

	v.decisions = widget.NewList(
		func() int { return len(v.run.Decisions()) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			notes := v.run.Decisions()
			if i < 0 || i >= len(notes) {
				return
			}
			if l, ok := o.(*widget.Label); ok {
				l.SetText(notes[i].Describe())
			}
		},
	)

	changes := widget.NewList(
		func() int { v.mu.RLock(); defer v.mu.RUnlock(); return len(v.changes) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			v.mu.RLock()
			defer v.mu.RUnlock()
			if i < 0 || i >= len(v.changes) {
				return
			}
			if l, ok := o.(*widget.Label); ok {
				l.SetText(v.changes[i].Describe())
			}
		},
	)

	lower := container.NewAppTabs(
		container.NewTabItemWithIcon("Decisions", theme.WarningIcon(), v.decisions),
		container.NewTabItemWithIcon("Since last run", theme.HistoryIcon(), changes),
	)

	split := container.NewVSplit(v.table, lower)
	split.Offset = 0.68

	v.content = container.NewBorder(counterBar, nil, nil, nil, split)
}

// Compare loads a previous run and populates the comparison tab.
func (v *CrawlView) Compare(prev crawlrun.Snapshot) {
	changes := crawlrun.Compare(prev, v.run.Rows())
	v.mu.Lock()
	v.changes = changes
	v.mu.Unlock()
	v.dirty.Store(true)
}

func (v *CrawlView) toggleFilter(key string, s crawlrun.State) {
	v.mu.Lock()
	if v.filter.active && v.filter.state == s {
		v.filter = filterState{}
	} else {
		v.filter = filterState{active: true, state: s}
	}
	v.mu.Unlock()
	v.dirty.Store(true)
	_ = key
}

func (v *CrawlView) clearFilter() {
	v.mu.Lock()
	v.filter = filterState{}
	v.mu.Unlock()
	v.dirty.Store(true)
}

func (v *CrawlView) sortBy(key string) {
	v.mu.Lock()
	if v.sortKey == key {
		v.sortAsc = !v.sortAsc
	} else {
		v.sortKey, v.sortAsc = key, true
	}
	v.mu.Unlock()
	v.dirty.Store(true)
}

func (v *CrawlView) rowAt(i int) (crawlrun.DeviceRow, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if i < 0 || i >= len(v.rows) {
		return crawlrun.DeviceRow{}, false
	}
	return v.rows[i], true
}

func (v *CrawlView) tableSize() (int, int) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.rows), len(crawlColumns)
}

func (v *CrawlView) makeCell() fyne.CanvasObject {
	l := widget.NewLabel("")
	l.Truncation = fyne.TextTruncateEllipsis
	return l
}

func (v *CrawlView) updateCell(id widget.TableCellID, o fyne.CanvasObject) {
	l, ok := o.(*widget.Label)
	if !ok {
		return
	}
	row, ok := v.rowAt(id.Row)
	if !ok {
		l.SetText("")
		return
	}
	l.TextStyle = fyne.TextStyle{}
	switch id.Col {
	case 0:
		l.SetText(row.Display())
	case 1:
		l.SetText(fmt.Sprint(row.Depth))
	case 2:
		l.SetText(row.Platform)
	case 3:
		l.SetText(row.State.String())
		// Not dialed is not a failure and must not read like one, or the
		// distinction the counters draw gets undone by the styling.
		l.TextStyle = fyne.TextStyle{Italic: row.State == crawlrun.StateNotDialed}
	case 4:
		if row.Credential == "" {
			l.SetText("")
		} else if row.CredReason != "" && row.CredReason != "pinned" {
			l.SetText(row.Credential + " (" + row.CredReason + ")")
		} else {
			l.SetText(row.Credential)
		}
	case 5:
		if row.Attempts > 1 {
			// More than one rung means failed authentications were spent.
			l.SetText(fmt.Sprintf("%d !", row.Attempts))
			l.TextStyle = fyne.TextStyle{Bold: true}
		} else if row.Attempts == 1 {
			l.SetText("1")
		} else {
			l.SetText("")
		}
	case 6:
		if row.Neighbors == 0 {
			l.SetText("")
		} else {
			l.SetText(fmt.Sprintf("%d/%d", row.New, row.Neighbors))
		}
	case 7:
		// Blank for a seed, which is the honest answer: nothing reported it,
		// you did.
		l.SetText(row.Via)
	case 8:
		if d := row.Duration(); d > 0 {
			l.SetText(d.Round(100 * time.Millisecond).String())
		} else {
			l.SetText("")
		}
	case 9:
		l.SetText(row.Detail)
	}
}

// redrawLoop coalesces change signals into a fixed refresh cadence.
func (v *CrawlView) redrawLoop() {
	// Wait for the driver. See Start.
	select {
	case <-v.started:
	case <-time.After(30 * time.Second):
		return
	}

	t := time.NewTicker(redrawInterval)
	defer t.Stop()
	for range t.C {
		if v.stopping.Load() {
			return
		}
		if !v.dirty.Swap(false) {
			continue
		}
		v.refresh()
	}
}

func (v *CrawlView) refresh() {
	v.mu.RLock()
	key, asc, filter := v.sortKey, v.sortAsc, v.filter
	v.mu.RUnlock()

	rows := v.run.Sorted(key, asc)
	if filter.active {
		kept := rows[:0]
		for _, r := range rows {
			if r.State == filter.state {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	counts := v.run.Counts()
	summary := fmt.Sprintf("depth %d  ·  %d devices  ·  %.2f tries/device  ·  %s",
		v.run.Depth(), counts.Total(), counts.AttemptsPerReached(),
		v.run.Elapsed().Round(time.Second))

	v.mu.Lock()
	v.rows = rows
	v.mu.Unlock()

	// Everything below touches widgets, so it belongs on the main thread.
	fyne.Do(func() {
		v.counters["reached"].SetText(fmt.Sprintf("Reached %d", counts.Reached))
		v.counters["failed"].SetText(fmt.Sprintf("Failed %d", counts.Failed))
		v.counters["notdialed"].SetText(fmt.Sprintf("Not dialed %d", counts.NotDialed))
		v.summary.SetText(summary)
		v.table.Refresh()
		v.decisions.Refresh()
	})
}
