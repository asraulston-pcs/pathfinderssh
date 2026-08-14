// internal/ui/captureview.go
//
// The capture window: what just happened, and what we hold.
//
// The crawl view has one surface because a crawl leaves no durable record —
// the run IS the artifact, and the comparison tab exists only because a
// snapshot file was invented to have something to diff against. Capture is the
// other way round. The run is transient and the store outlives it, so the
// window has two peer tabs over the same subject:
//
//	Run     the (device, capture type) pairs of the capture that is running
//	Store   what the store holds, browsable with no run at all
//
// The Store tab does not depend on a Run. Opening this window against a store
// and reading last night's config, with nothing capturing, is a legitimate
// session and probably the common one once captures are scheduled.
//
// # The jump is the point
//
// Clicking a row in the Run tab selects that device and type in the Store tab.
// Without it these are two programs sharing a binary; with it a capture that
// just ran is one click from the file it wrote.
//
// # Why the device list does not show per-type detail
//
// It could: Types() returns attempts, versions and last-captured per type. But
// history.jsonl records every attempt including unchanged ones, and reading it
// means parsing every line — so a list that showed per-type counts would cost
// devices x types x history-depth on the one screen that opens first. A
// nightly schedule with four types writes about 1,460 lines per type per year.
//
// device.json already carries LastSeen, and Put updates it before the dedup
// branch, so it moves on unchanged captures too. That is one small read per
// device for the column that matters. Per-type numbers belong to the device
// you selected, which is where they are.
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

	"github.com/scottpeterman/pathfinderssh/internal/capture"
	"github.com/scottpeterman/pathfinderssh/internal/capturerun"
)

// captureRedrawInterval is the run table's refresh cadence.
const captureRedrawInterval = 200 * time.Millisecond

var captureColumns = []struct {
	title string
	key   string
	width float32
}{
	{"Device", "device", 210},
	{"Type", "type", 130},
	{"Platform", "platform", 110},
	{"State", "state", 110},
	{"Bytes", "bytes", 80},
	{"Time", "duration", 70},
	// Wide because the values that land here are dial errors and command
	// timeouts, and a truncated reason sends the reader to the decisions
	// pane for something the row already knew.
	{"Detail", "detail", 520},
}

// CaptureView renders a capture run and the store it writes to.
//
// It constructs widgets, so it must be built after app.New(). Its redraw loop
// is gated by Start(), which the host calls immediately before ShowAndRun.
type CaptureView struct {
	run   *capturerun.Run
	store capture.Browser // may be nil: the Run tab works without one

	// OnOpenExternal is called when a capture has been opened. Nil means
	// the built-in content pane is the whole story, which is the default;
	// a shell might route the file to an editor instead.
	OnOpenExternal func(path string)

	mu      sync.RWMutex
	rows    []capturerun.Row
	filter  captureFilter
	sortKey string
	sortAsc bool

	dirty    atomic.Bool
	stopping atomic.Bool
	started  chan struct{}

	// Run tab.
	table     *widget.Table
	decisions *widget.List
	counters  map[string]*widget.Button
	summary   *widget.Label

	// Store tab. Selection state is held here rather than read back out of
	// the widgets, because a List's selection is cleared by Refresh and the
	// three panes have to agree about which device is showing.
	devices   []capture.DeviceInfo
	types     []capture.TypeInfo
	versions  []capture.HistoryEntry
	selDevice string
	selType   string
	// storeStatus carries warnings and errors and is NOT overwritten by
	// normal activity. storeCaption carries what is currently shown.
	// One label for both meant a damaged-store warning was erased by the
	// next file someone opened — and that warning only ever fires when
	// something is actually wrong.
	storeStatus  *widget.Label
	storeCaption *widget.Label
	deviceList   *widget.List
	typeList     *widget.List
	versionList  *widget.List
	content      *widget.TextGrid

	tabs *container.AppTabs
	root fyne.CanvasObject
}

type captureFilter struct {
	active bool
	state  capturerun.State
}

// NewCaptureView builds the view. store may be nil.
//
// This constructs widgets: call it after app.New(), never before. See
// README_Fyne_UI.md — building a widget without a current app nil-derefs inside
// Button.CreateRenderer and the panic names a layout function.
func NewCaptureView(run *capturerun.Run, store capture.Browser) *CaptureView {
	v := &CaptureView{
		run:      run,
		store:    store,
		sortKey:  "device",
		sortAsc:  true,
		counters: map[string]*widget.Button{},
		started:  make(chan struct{}),
	}
	v.build()

	// The engine emits from every worker; Fyne is single-threaded. Set the
	// flag here and let the ticker do the toolkit work on the main thread.
	run.OnChange(func() { v.dirty.Store(true) })
	go v.redrawLoop()

	return v
}

// Content is the object to place in a window.
func (v *CaptureView) Content() fyne.CanvasObject { return v.root }

// Emit is the hook to hand capturedial.Options.
func (v *CaptureView) Emit() capturerun.Emit { return v.run.Emit() }

// Start releases the redraw loop. Call it immediately before ShowAndRun: the
// loop calls fyne.Do, and there is no driver to hand work to before then.
func (v *CaptureView) Start() {
	select {
	case <-v.started:
	default:
		close(v.started)
	}
}

// Stop ends the redraw loop. Call it when the view is discarded.
func (v *CaptureView) Stop() { v.stopping.Store(true) }

// ---------------------------------------------------------------- construction

func (v *CaptureView) build() {
	runTab := container.NewTabItemWithIcon("Run", theme.MediaPlayIcon(), v.buildRun())
	storeTab := container.NewTabItemWithIcon("Store", theme.StorageIcon(), v.buildStore())
	v.tabs = container.NewAppTabs(runTab, storeTab)

	// Reload on activation rather than per event. A capture writing while
	// the tab is open makes the listing stale, but redrawing a directory
	// walk on every stored artifact spends the frame budget on a pane
	// nobody is looking at.
	v.tabs.OnSelected = func(t *container.TabItem) {
		if t == storeTab {
			v.reloadDevices()
		}
	}
	v.root = v.tabs
}

func (v *CaptureView) buildRun() fyne.CanvasObject {
	v.summary = widget.NewLabel("")

	bar := container.NewHBox()
	for _, c := range []struct {
		key   string
		label string
		state capturerun.State
	}{
		{"stored", "Stored", capturerun.StateStored},
		{"unchanged", "Unchanged", capturerun.StateUnchanged},
		{"napplic", "Not applicable", capturerun.StateNotApplicable},
		{"failed", "Failed", capturerun.StateFailed},
	} {
		state := c.state
		b := widget.NewButton(c.label+" 0", func() { v.toggleFilter(state) })
		b.Importance = widget.LowImportance
		v.counters[c.key] = b
		bar.Add(b)
	}
	all := widget.NewButton("All", func() { v.clearFilter() })
	all.Importance = widget.LowImportance
	bar.Add(all)
	bar.Add(v.summary)

	v.table = widget.NewTable(v.runTableSize, v.makeRunCell, v.updateRunCell)
	v.table.ShowHeaderRow = true
	v.table.CreateHeader = func() fyne.CanvasObject { return widget.NewButton("", nil) }
	v.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		b, ok := o.(*widget.Button)
		if !ok || id.Col < 0 || id.Col >= len(captureColumns) {
			return
		}
		col := captureColumns[id.Col]
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
	for i, c := range captureColumns {
		v.table.SetColumnWidth(i, c.width)
	}
	v.table.OnSelected = func(id widget.TableCellID) {
		row, ok := v.rowAt(id.Row)
		v.table.UnselectAll()
		if !ok {
			return
		}
		v.jumpToStore(row)
	}

	v.decisions = widget.NewList(
		func() int { return len(v.run.Decisions()) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			d := v.run.Decisions()
			if i < 0 || i >= len(d) {
				return
			}
			if l, ok := o.(*widget.Label); ok {
				l.SetText(d[i].Describe())
			}
		},
	)

	split := container.NewVSplit(v.table, v.decisions)
	split.Offset = 0.72
	return container.NewBorder(bar, nil, nil, nil, split)
}

func (v *CaptureView) buildStore() fyne.CanvasObject {
	v.storeStatus = widget.NewLabel("")
	v.storeCaption = widget.NewLabel("")

	v.deviceList = widget.NewList(
		func() int { v.mu.RLock(); defer v.mu.RUnlock(); return len(v.devices) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			v.mu.RLock()
			defer v.mu.RUnlock()
			if i < 0 || i >= len(v.devices) {
				return
			}
			if l, ok := o.(*widget.Label); ok {
				l.SetText(describeDevice(v.devices[i]))
			}
		},
	)
	v.deviceList.OnSelected = func(i widget.ListItemID) {
		v.mu.RLock()
		var name string
		if i >= 0 && i < len(v.devices) {
			name = v.devices[i].Canonical
		}
		v.mu.RUnlock()
		if name != "" {
			v.selectDevice(name, "")
		}
	}

	v.typeList = widget.NewList(
		func() int { v.mu.RLock(); defer v.mu.RUnlock(); return len(v.types) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			v.mu.RLock()
			defer v.mu.RUnlock()
			if i < 0 || i >= len(v.types) {
				return
			}
			if l, ok := o.(*widget.Label); ok {
				l.SetText(describeType(v.types[i]))
			}
		},
	)
	v.typeList.OnSelected = func(i widget.ListItemID) {
		v.mu.RLock()
		var typ string
		if i >= 0 && i < len(v.types) {
			typ = v.types[i].Type
		}
		v.mu.RUnlock()
		if typ != "" {
			v.selectType(typ)
		}
	}

	v.versionList = widget.NewList(
		func() int { v.mu.RLock(); defer v.mu.RUnlock(); return len(v.versions) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			v.mu.RLock()
			defer v.mu.RUnlock()
			if i < 0 || i >= len(v.versions) {
				return
			}
			l, ok := o.(*widget.Label)
			if !ok {
				return
			}
			e := v.versions[i]
			l.SetText(describeVersion(e))
			// An unchanged attempt wrote no file. It is not a failure
			// and must not read like one; italic says "nothing new
			// here" without saying "something went wrong".
			l.TextStyle = fyne.TextStyle{Italic: e.Unchanged}
		},
	)
	v.versionList.OnSelected = func(i widget.ListItemID) {
		v.mu.RLock()
		var file string
		if i >= 0 && i < len(v.versions) {
			file = v.versions[i].File
		}
		dev, typ := v.selDevice, v.selType
		v.mu.RUnlock()
		v.openVersion(dev, typ, file)
	}

	v.content = widget.NewTextGrid()

	reload := widget.NewButtonWithIcon("Reload", theme.ViewRefreshIcon(), func() {
		v.reloadDevices()
	})
	reload.Importance = widget.LowImportance

	lists := container.NewHSplit(
		labelled("Devices", v.deviceList),
		container.NewHSplit(
			labelled("Types", v.typeList),
			labelled("Versions", v.versionList),
		),
	)
	lists.Offset = 0.34

	split := container.NewVSplit(lists, container.NewScroll(v.content))
	split.Offset = 0.42

	bar := container.NewHBox(reload, v.storeCaption, v.storeStatus)
	return container.NewBorder(bar, nil, nil, nil, split)
}

func labelled(title string, obj fyne.CanvasObject) fyne.CanvasObject {
	h := widget.NewLabel(title)
	h.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewBorder(h, nil, nil, nil, obj)
}

// ------------------------------------------------------------------- run tab

func (v *CaptureView) toggleFilter(s capturerun.State) {
	v.mu.Lock()
	if v.filter.active && v.filter.state == s {
		v.filter = captureFilter{}
	} else {
		v.filter = captureFilter{active: true, state: s}
	}
	v.mu.Unlock()
	v.dirty.Store(true)
}

func (v *CaptureView) clearFilter() {
	v.mu.Lock()
	v.filter = captureFilter{}
	v.mu.Unlock()
	v.dirty.Store(true)
}

func (v *CaptureView) sortBy(key string) {
	v.mu.Lock()
	if v.sortKey == key {
		v.sortAsc = !v.sortAsc
	} else {
		v.sortKey, v.sortAsc = key, true
	}
	v.mu.Unlock()
	v.dirty.Store(true)
}

func (v *CaptureView) rowAt(i int) (capturerun.Row, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if i < 0 || i >= len(v.rows) {
		return capturerun.Row{}, false
	}
	return v.rows[i], true
}

func (v *CaptureView) runTableSize() (int, int) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.rows), len(captureColumns)
}

func (v *CaptureView) makeRunCell() fyne.CanvasObject {
	l := widget.NewLabel("")
	l.Truncation = fyne.TextTruncateEllipsis
	return l
}

func (v *CaptureView) updateRunCell(id widget.TableCellID, o fyne.CanvasObject) {
	l, ok := o.(*widget.Label)
	if !ok {
		return
	}
	row, ok := v.rowAt(id.Row)
	if !ok {
		l.SetText("")
		return
	}
	// Cells are reused, so any conditional style has to be reset or a bold
	// row smears onto whatever scrolls into its place.
	l.TextStyle = fyne.TextStyle{}
	switch id.Col {
	case 0:
		l.SetText(row.Display())
	case 1:
		l.SetText(row.Type)
	case 2:
		l.SetText(row.Platform)
	case 3:
		l.SetText(row.State.String())
		// Bold means look here. Italic means this pair is not part of
		// this device's world. Unchanged is left plain on purpose: it
		// is the majority of rows on a healthy run, and a screen where
		// most rows are marked is a screen with no marks.
		switch row.State {
		case capturerun.StateFailed:
			l.TextStyle = fyne.TextStyle{Bold: true}
		case capturerun.StateNotApplicable:
			l.TextStyle = fyne.TextStyle{Italic: true}
		}
	case 4:
		if row.Bytes > 0 {
			l.SetText(fmt.Sprintf("%d", row.Bytes))
		} else {
			l.SetText("")
		}
	case 5:
		if d := row.Duration(); d > 0 {
			l.SetText(d.Round(100 * time.Millisecond).String())
		} else {
			l.SetText("")
		}
	case 6:
		l.SetText(row.Detail)
	}
}

func (v *CaptureView) redrawLoop() {
	select {
	case <-v.started:
	case <-time.After(30 * time.Second):
		return
	}
	t := time.NewTicker(captureRedrawInterval)
	defer t.Stop()

	// The first store read happens here rather than in Start, and that is
	// the whole reason it is here. reloadDevices ends in fyne.Do, and Start
	// runs on the line before ShowAndRun — so calling it from Start races
	// the driver into existence. Waiting for the first tick puts it safely
	// after the app is running, at the cost of 200ms nobody will see.
	first := true

	for range t.C {
		if v.stopping.Load() {
			return
		}
		if first {
			first = false
			v.reloadDevices()
		}
		if !v.dirty.Swap(false) {
			continue
		}
		v.refresh()
	}
}

func (v *CaptureView) refresh() {
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
	c := v.run.Counts()
	summary := fmt.Sprintf("%d device(s) · %d pair(s) · %s stored · %s",
		c.Devices, len(v.run.Rows()), humanBytes(c.BytesStored),
		v.run.Elapsed().Round(time.Second))

	v.mu.Lock()
	v.rows = rows
	v.mu.Unlock()

	fyne.Do(func() {
		v.counters["stored"].SetText(fmt.Sprintf("Stored %d", c.Stored))
		v.counters["unchanged"].SetText(fmt.Sprintf("Unchanged %d", c.Unchanged))
		v.counters["napplic"].SetText(fmt.Sprintf("Not applicable %d", c.NotApplicable))
		v.counters["failed"].SetText(fmt.Sprintf("Failed %d", c.Failed))
		v.summary.SetText(summary)
		v.table.Refresh()
		v.decisions.Refresh()
	})
}

// ----------------------------------------------------------------- store tab

// jumpToStore selects the row's device and type in the Store tab.
//
// Keyed on Name rather than Display(): the store files a device under its
// canonical name, and a row that was never named has nothing in the store to
// jump to. Saying so is better than opening the tab on the wrong device.
func (v *CaptureView) jumpToStore(row capturerun.Row) {
	if v.store == nil {
		return
	}
	if row.Name == "" {
		v.setStatus(fmt.Sprintf("%s was never named, so nothing was filed for it", row.Display()))
		return
	}
	fyne.Do(func() { v.tabs.SelectIndex(1) })
	v.selectDevice(row.Name, row.Type)
}

// setStatus reports something wrong. It sticks until the next problem or the
// next successful reload.
func (v *CaptureView) setStatus(msg string) {
	fyne.Do(func() { v.storeStatus.SetText(msg) })
}

// setCaption reports what is currently on screen. Never an error.
func (v *CaptureView) setCaption(msg string) {
	fyne.Do(func() { v.storeCaption.SetText(msg) })
}

// reloadDevices refreshes the device list. Cheap by construction: one
// device.json per device, no history parsed.
func (v *CaptureView) reloadDevices() {
	if v.store == nil {
		v.setStatus("no store; pass -store to browse captures")
		return
	}
	go func() {
		devs, err := v.store.Devices()

		// A partial list with an error is the normal result when a
		// device directory is damaged. Show the devices that read AND
		// say which did not — a browser that renders nothing because
		// one directory is broken is worse than one that says so.
		status := ""
		if err != nil {
			status = err.Error()
		} else if len(devs) == 0 {
			status = "store is empty"
		}

		v.mu.Lock()
		v.devices = devs
		v.mu.Unlock()

		fyne.Do(func() {
			v.storeStatus.SetText(status)
			v.deviceList.Refresh()
		})
	}()
}

// selectDevice loads a device's types, and optionally selects one of them.
//
// There is deliberately NO "already selected, nothing to do" guard here. There
// was one, and it was a bug: reloadDevices calls deviceList.Refresh(), which
// drops the widget's selection while selDevice still holds the last device, so
// clicking that same device again did nothing at all — no types, no versions,
// no config. It worked on the first visit to the tab and died after any Reload
// or tab switch, which is the worst shape a bug can have.
//
// Reloading a device already showing costs one directory read. Being unable to
// click a device costs the tab.
func (v *CaptureView) selectDevice(canonical, wantType string) {
	v.mu.Lock()
	v.selDevice, v.selType = canonical, ""
	v.types, v.versions = nil, nil
	v.mu.Unlock()

	fyne.Do(func() {
		v.typeList.UnselectAll()
		v.versionList.UnselectAll()
		v.content.SetText("")
		v.typeList.Refresh()
		v.versionList.Refresh()
	})
	go v.loadTypes(canonical, wantType)
}

// loadTypes reads one device's per-type summaries. This is the read that was
// kept off the device list; scoped to a single device it is bounded by that
// device's own history rather than by the size of the estate.
func (v *CaptureView) loadTypes(canonical, then string) {
	if v.store == nil {
		return
	}
	types, err := v.store.Types(canonical)
	if err != nil {
		v.setStatus(err.Error())
		return
	}
	v.mu.Lock()
	if v.selDevice != canonical {
		// The selection moved on while this read was in flight.
		v.mu.Unlock()
		return
	}
	v.types = types
	v.mu.Unlock()

	// A device with exactly one capture type selects it. Requiring a click
	// on a one-item list to reach the versions is friction for no decision,
	// and it leaves a state where versions can be on screen with no type
	// selected — which is how a version click ends up discarded.
	if then == "" && len(types) == 1 {
		then = types[0].Type
	}

	at := -1
	for i, t := range types {
		if t.Type == then {
			at = i
			break
		}
	}
	fyne.Do(func() {
		v.typeList.Refresh()
		// Select in the WIDGET too, not just in our own state. The two
		// disagreeing is the same shape as the device-selection bug:
		// something is selected as far as the code is concerned and
		// nothing is highlighted as far as the reader is concerned.
		if at >= 0 {
			v.typeList.Select(at)
		}
	})
	if then != "" {
		v.selectType(then)
	}
}

func (v *CaptureView) selectType(typ string) {
	v.mu.Lock()
	dev := v.selDevice
	v.selType = typ
	v.mu.Unlock()
	if dev == "" || v.store == nil {
		return
	}
	go func() {
		hist, err := v.store.History(dev, typ)
		if err != nil {
			v.setStatus(err.Error())
			return
		}
		// Newest first: the question is almost always what ran last.
		rev := make([]capture.HistoryEntry, 0, len(hist))
		for i := len(hist) - 1; i >= 0; i-- {
			rev = append(rev, hist[i])
		}
		v.mu.Lock()
		if v.selDevice != dev || v.selType != typ {
			v.mu.Unlock()
			return
		}
		v.versions = rev
		v.mu.Unlock()

		fyne.Do(func() {
			v.versionList.UnselectAll()
			v.versionList.Refresh()
		})
	}()
}

func (v *CaptureView) openVersion(canonical, typ, file string) {
	// Report rather than return. A click that produces nothing at all —
	// no content, no message — is indistinguishable from a broken read,
	// and this guard silently swallowed every version click made before a
	// capture type was selected.
	if v.store == nil {
		v.setStatus("no store; pass -store to browse captures")
		return
	}
	if canonical == "" || typ == "" {
		v.setStatus("select a device and a capture type first")
		return
	}
	if file == "" {
		v.setStatus("this attempt recorded no file")
		return
	}
	go func() {
		body, err := v.store.Read(canonical, typ, file)
		if err != nil {
			v.setStatus(err.Error())
			fyne.Do(func() { v.content.SetText("") })
			return
		}
		if v.OnOpenExternal != nil {
			v.OnOpenExternal(file)
		}
		text := string(body)
		v.setCaption(fmt.Sprintf("%s · %s · %s", canonical, typ, file))
		fyne.Do(func() { v.content.SetText(text) })
	}()
}

// ------------------------------------------------------------------ rendering

func describeDevice(d capture.DeviceInfo) string {
	s := d.Canonical
	if d.Platform != "" {
		s += "  (" + d.Platform + ")"
	}
	if !d.LastSeen.IsZero() {
		s += "  ·  " + ago(d.LastSeen)
	}
	return s
}

func describeType(t capture.TypeInfo) string {
	// Versions and attempts are separate numbers on purpose. "4 versions"
	// on a device captured nightly for a year is not a device that was
	// captured four times; it is a device whose config changed four times,
	// which is the more useful fact and the one a file count would hide.
	s := fmt.Sprintf("%s  ·  %d version(s) of %d capture(s)", t.Type, t.Stored, t.Attempts)
	if !t.Last.IsZero() {
		s += "  ·  " + ago(t.Last)
	}
	return s
}

func describeVersion(e capture.HistoryEntry) string {
	s := e.At.Local().Format("2006-01-02 15:04:05")
	if e.Unchanged {
		return s + "  ·  unchanged"
	}
	return fmt.Sprintf("%s  ·  %s", s, humanBytes(e.Bytes))
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
