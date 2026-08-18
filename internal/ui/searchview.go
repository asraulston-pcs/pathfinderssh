// internal/ui/searchview.go
//
// The search applet: a hit list over the file each hit came from.
//
// # Single click, not double
//
// Selecting a hit loads its artifact into the lower pane and moves to the
// matching line. There is no double-click anywhere here, for two reasons.
// widget.Table has no per-row double-tap callback — cells come from CreateCell,
// so double-click means a custom fyne.DoubleTappable cell type — and every
// other table and list in this package already drives its action from
// OnSelected. A search result list where selecting a row shows the result is
// also simply what people expect; the alternative is a list where clicking
// does nothing and the user has to discover that clicking twice does.
//
// # Why the content pane is a RichText and not a TextGrid
//
// TextGrid builds a canvas object per RUNE, and it only renders a visible
// window when its own Scroll field is set — which this pane cannot use,
// because the internal scroller is unexported and jumping to the matching line
// needs an offset nobody can set. In an external container.Scroll it is
// therefore ScrollNone, and every cell of the whole file exists at once.
// Measured on one Arista config: 261,865 objects at 700 lines and 1,119,765 at
// 3,000. That is what made the pane hang the UI.
//
// RichText builds one object per SEGMENT. One segment per line is 718 objects
// at 700 lines and 3,018 at 3,000 — the same file, 365x fewer objects — and it
// keeps the external scroll, so scroll-to-the-line still works. Per-segment
// styling also keeps SUBSTRING highlighting, which a list of Labels would lose.
//
// # Where the line numbers come from
//
// RichText has no PositionForCursorLocation, so the row height is derived as
// MinSize().Height divided by the row count — from the widget's OWN layout
// rather than a metric reimplemented here. With wrapping off and one segment
// per line every row is the same height, so it is exact, and it self-corrects
// at any font size. The terminal carries a long note about what happens when
// two places in one process hold separate opinions about how tall a row is.
package ui

import (
	"fmt"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/capture"
	"github.com/scottpeterman/pathfinderssh/internal/storesearch"
)

// Column widths. The match text takes what is left because it is the column
// people read; the rest are sized to their content.
var searchColumnWidths = []float32{200, 150, 70, 620}

const searchColumns = 4

// SearchView renders one search.
//
// It knows nothing about how a search is run or what a session is. The host
// hands it results and installs OnConnect; the view calls back out.
type SearchView struct {
	browser capture.Browser

	root    fyne.CanvasObject
	table   *widget.Table
	connect *widget.Button
	status  *widget.Label
	header  *widget.Label
	content *widget.RichText
	scroll  *container.Scroll

	// bar reports that a scan is running, and how far along it is.
	//
	// It is a widget rather than more text in the header because "is this
	// still going" was being answered by reading a word inside a line that
	// also holds the query and the summary — and by the Stop button's
	// disabled state, which is the wrong signal twice over: disabled
	// styling is deliberately low-contrast, and it means "you cannot press
	// this" rather than "your answer is ready". Presence is easier to read
	// than prose: the bar is there while it runs and gone when it stops.
	bar *widget.ProgressBar

	loop *redraw

	mu       sync.RWMutex
	hits     []storesearch.Hit
	result   storesearch.Result
	desc     string
	progress string
	failed   string
	needle   string
	fold     bool

	// running is what the bar's visibility follows. It is set by the host
	// rather than inferred from progress, because a scan that has started
	// but not yet reported a device is still running, and a scan of an
	// empty store may never report one at all.
	running   bool
	scanDone  int
	scanTotal int

	// selected is the hit whose artifact is in the content pane, by index
	// into hits. -1 for none.
	selected int
	// contentRows is how many lines the pane currently holds, which is what
	// turns a line number into a scroll offset.
	contentRows int

	// pendingScroll is a 1-based line the pane still owes a jump to.
	//
	// The jump cannot happen in the same pass that installs the content:
	// MinSize is the laid-out height and it has not been recomputed yet, so
	// ScrollToOffset clamps against a stale (usually zero) content height
	// and the pane stays at the top. Handing it to the redraw tick puts it
	// one interval later, after the layout pass, which is the same reason
	// the capture view moved its first store read onto the first tick.
	pendingScroll int

	// loadedFor identifies the artifact currently in the pane, so
	// selecting another hit in the same file scrolls instead of re-reading
	// a config that is already on screen.
	loadedFor string

	// OnConnect is called with a device name when the operator asks to
	// open a session on the device a hit came from. Nil disables the
	// button — a control that cannot do anything is worse than no control.
	OnConnect func(device string)
}

// NewSearchView builds the view. It constructs widgets, so it must be called
// after app.New().
func NewSearchView(browser capture.Browser) *SearchView {
	v := &SearchView{browser: browser, selected: -1}
	v.build()
	v.loop = newRedraw(v.refresh)
	go v.loop.run()
	return v
}

// Content implements Applet.
func (v *SearchView) Content() fyne.CanvasObject { return v.root }

// Start implements Applet.
func (v *SearchView) Start() { v.loop.start() }

// Stop implements Applet.
func (v *SearchView) Stop() { v.loop.stop() }

func (v *SearchView) build() {
	v.header = widget.NewLabel("")
	v.header.TextStyle = fyne.TextStyle{Bold: true}
	v.status = widget.NewLabel("")
	v.status.Wrapping = fyne.TextWrapOff

	v.table = widget.NewTable(
		func() (int, int) {
			v.mu.RLock()
			defer v.mu.RUnlock()
			return len(v.hits), searchColumns
		},
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			l, ok := o.(*widget.Label)
			if !ok {
				return
			}
			// Cells are reused, so every style set on any path must
			// be reset on every path. A style that is only ever
			// applied leaks onto whichever row scrolls into that
			// cell next.
			l.TextStyle = fyne.TextStyle{}

			v.mu.RLock()
			var h storesearch.Hit
			if id.Row >= 0 && id.Row < len(v.hits) {
				h = v.hits[id.Row]
			}
			v.mu.RUnlock()

			switch id.Col {
			case 0:
				l.SetText(h.Device)
			case 1:
				l.SetText(h.Type)
			case 2:
				l.SetText(fmt.Sprintf("%d", h.Line))
			default:
				// Monospace, because a matching config line
				// read next to the one above it is the whole
				// point of the column.
				l.TextStyle = fyne.TextStyle{Monospace: true}
				l.SetText(h.Text)
			}
			l.Refresh()
		},
	)
	v.table.ShowHeaderRow = true
	v.table.CreateHeader = func() fyne.CanvasObject { return widget.NewLabel("") }
	v.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		l, ok := o.(*widget.Label)
		if !ok {
			return
		}
		l.TextStyle = fyne.TextStyle{Bold: true}
		switch id.Col {
		case 0:
			l.SetText("Device")
		case 1:
			l.SetText("Type")
		case 2:
			l.SetText("Line")
		default:
			l.SetText("Match")
		}
	}
	for i, w := range searchColumnWidths {
		v.table.SetColumnWidth(i, w)
	}
	v.table.OnSelected = func(id widget.TableCellID) { v.selectHit(id.Row) }

	// Wrapping OFF and Scroll left at ScrollNone: the wrapper below owns
	// the scrolling, because RichText's own scroller is unexported and this
	// pane has to be able to jump to a line.
	v.content = widget.NewRichText()
	v.content.Wrapping = fyne.TextWrapOff
	v.scroll = container.NewScroll(v.content)

	connect := widget.NewButtonWithIcon("Open session", theme.ComputerIcon(), v.connectSelected)
	connect.Disable()
	v.connect = connect

	// Hidden until a scan starts. TextFormatter rather than the default
	// percentage: "scanning 42/106 devices" is the thing being waited on,
	// and a percentage of an unknown-sized store means nothing.
	v.bar = widget.NewProgressBar()
	v.bar.TextFormatter = func() string {
		v.mu.RLock()
		done, total := v.scanDone, v.scanTotal
		v.mu.RUnlock()
		if total <= 0 {
			return "scanning…"
		}
		return fmt.Sprintf("scanning %d/%d devices", done, total)
	}
	v.bar.Hide()

	top := container.NewBorder(container.NewVBox(v.header, v.bar), nil, nil, nil, v.table)
	bottom := container.NewBorder(
		container.NewBorder(nil, nil, nil, connect, v.status), nil, nil, nil, v.scroll)

	split := container.NewVSplit(top, bottom)
	split.Offset = 0.55
	v.root = split
}

func (v *SearchView) connectSelected() {
	v.mu.RLock()
	i, hits, fn := v.selected, v.hits, v.OnConnect
	v.mu.RUnlock()
	if fn == nil || i < 0 || i >= len(hits) {
		// Never a silent return in this package: a click that does
		// nothing and says nothing is indistinguishable from a broken
		// one, which cost two rounds in the capture store tab.
		v.setStatusNow("select a hit first")
		return
	}
	fn(hits[i].Device)
}

// SetMatcher tells the view what is being looked for, so highlighting agrees
// with the matcher instead of holding a second opinion about it.
func (v *SearchView) SetMatcher(m storesearch.Matcher) {
	v.mu.Lock()
	v.desc = ""
	v.needle, v.fold = "", false
	if m != nil {
		v.desc = m.Describe()
		if l, ok := m.(*storesearch.Literal); ok {
			v.needle, v.fold = l.Needle(), l.Folds()
		}
	}
	v.mu.Unlock()
	v.loop.mark()
}

// SetProgress updates the status line while a scan runs. Safe from the scan's
// own goroutines.
func (v *SearchView) SetProgress(done, total int) {
	v.mu.Lock()
	v.progress = fmt.Sprintf("scanning %d/%d…", done, total)
	v.scanDone, v.scanTotal = done, total
	v.mu.Unlock()
	v.loop.mark()
}

// SetRunning marks a scan as started or finished, which is what the progress
// bar's visibility follows.
//
// Separate from SetProgress and SetResult because neither can stand in for it.
// A scan that has started but not yet reported its first device is running
// with no progress to show, and a store with nothing in it may report no
// progress at all — so inferring "running" from the presence of a count would
// leave both of those looking finished before they were.
func (v *SearchView) SetRunning(on bool) {
	v.mu.Lock()
	v.running = on
	if on {
		v.scanDone, v.scanTotal = 0, 0
		v.progress = ""
	}
	v.mu.Unlock()
	v.loop.mark()
}

// Reset clears everything a previous search left behind, so the view can be
// re-run in place instead of the operator having to close the tab.
//
// loadedFor is the one that bites if it is forgotten: it is the cache key
// saying which artifact the lower pane holds, so leaving it set means the
// previous device's file stays on screen under the new hits, and selecting a
// hit in a DIFFERENT file that happens to share the key would scroll the old
// content rather than load the new.
func (v *SearchView) Reset() {
	v.mu.Lock()
	v.hits = nil
	v.result = storesearch.Result{}
	v.progress = ""
	v.failed = ""
	v.selected = -1
	v.loadedFor = ""
	v.contentRows = 0
	v.pendingScroll = 0
	v.scanDone, v.scanTotal = 0, 0
	v.mu.Unlock()

	fyne.Do(func() {
		v.content.Segments = nil
		v.content.Refresh()
		v.table.UnselectAll()
		v.connect.Disable()
		v.status.SetText("")
	})
	v.loop.mark()
}

// SetResult installs a finished search.
func (v *SearchView) SetResult(res storesearch.Result) {
	v.mu.Lock()
	v.result = res
	v.hits = res.Hits
	v.progress = ""
	v.failed = ""
	v.selected = -1
	v.loadedFor = ""
	v.mu.Unlock()
	v.loop.mark()
}

// SetError reports a search that could not run at all — a store that would not
// open, an empty query. Distinct from a search that ran and found nothing,
// which is a legitimate answer and must not look like a failure.
func (v *SearchView) SetError(err error) {
	v.mu.Lock()
	v.failed = err.Error()
	v.progress = ""
	v.mu.Unlock()
	v.loop.mark()
}

// setStatus is for callers on a background goroutine.
//
// fyne.Do called from the MAIN goroutine is not merely redundant: Fyne logs an
// error and then runs the closure with `go fn()`, moving UI work off the main
// thread — the opposite of what the call is for. So the two cases are separate
// methods rather than one that guesses.
func (v *SearchView) setStatus(msg string) {
	fyne.Do(func() { v.status.SetText(msg) })
}

// setStatusNow is for callers already on the main thread: click handlers, and
// anything already inside a fyne.Do.
func (v *SearchView) setStatusNow(msg string) { v.status.SetText(msg) }

// refresh runs on the redraw tick, on the main thread.
func (v *SearchView) refresh() {
	v.mu.Lock()
	desc, prog, failed, res := v.desc, v.progress, v.failed, v.result
	nHits := len(v.hits)
	jump := v.pendingScroll
	v.pendingScroll = 0
	running, done, total := v.running, v.scanDone, v.scanTotal
	v.mu.Unlock()

	head := desc
	switch {
	case failed != "":
		head = "search failed — " + failed
	case prog != "":
		head = strings.TrimSpace(desc + " · " + prog)
	case nHits > 0 || res.Artifacts > 0:
		head = strings.TrimSpace(desc + " · " + res.Summary())
	}

	fyne.Do(func() {
		v.header.SetText(head)
		// Value before visibility: a bar shown at its previous run's
		// fill for one tick reads as a scan that resumed part-done.
		if total > 0 {
			v.bar.Max = float64(total)
			v.bar.SetValue(float64(done))
		} else {
			v.bar.Max = 1
			v.bar.SetValue(0)
		}
		if running {
			v.bar.Show()
		} else {
			v.bar.Hide()
		}
		v.table.Refresh()
		if len(res.Skips) > 0 && prog == "" && failed == "" && jump == 0 {
			v.status.SetText(skipLine(res.Skips))
		}
		if jump > 0 {
			v.scrollToLine(jump)
		}
	})
}

// skipLine names skipped artifacts rather than counting them. A count says
// something went wrong; a name says which device to go and look at.
func skipLine(skips []storesearch.Skip) string {
	const show = 3
	var parts []string
	for i, s := range skips {
		if i == show {
			parts = append(parts, fmt.Sprintf("and %d more", len(skips)-show))
			break
		}
		parts = append(parts, s.String())
	}
	return "not searched: " + strings.Join(parts, "; ")
}

// selectHit loads the artifact behind a row and moves to its line.
func (v *SearchView) selectHit(row int) {
	v.mu.Lock()
	if row < 0 || row >= len(v.hits) {
		v.mu.Unlock()
		return
	}
	h := v.hits[row]
	v.selected = row
	already := v.loadedFor == artifactKey(h)
	needle, fold := v.needle, v.fold
	browser := v.browser
	v.mu.Unlock()

	// selectHit runs from Table.OnSelected, i.e. already on the main
	// thread. No fyne.Do anywhere on this path.
	if v.connect != nil && v.OnConnect != nil {
		v.connect.Enable()
	}

	if already {
		// Same file, so the layout is already settled — but go through
		// the same tick as a fresh load rather than keeping a second
		// path that only works sometimes.
		v.mu.Lock()
		v.pendingScroll = h.Line
		v.mu.Unlock()
		v.loop.mark()
		return
	}
	if browser == nil {
		v.setStatusNow("no store open — nothing to show")
		return
	}

	// Read off the UI thread. A config is small but a store can be on a
	// network mount, and a view that blocks the driver on a read is a view
	// that freezes the whole window including its own status line.
	go func() {
		data, err := browser.Read(h.Device, h.Type, h.File)
		if err != nil {
			v.setStatus(fmt.Sprintf("%s / %s: %v", h.Device, h.Type, err))
			return
		}
		text := string(data)

		v.mu.Lock()
		// The selection may have moved on while the read was in
		// flight. Painting anyway would put an older file over a newer
		// selection, which reads as the wrong file being opened.
		stale := v.selected < 0 || v.selected >= len(v.hits) ||
			artifactKey(v.hits[v.selected]) != artifactKey(h)
		if !stale {
			v.loadedFor = artifactKey(h)
		}
		v.mu.Unlock()
		if stale {
			return
		}

		segs, rows := configSegments(text, needle, fold)
		v.mu.Lock()
		v.contentRows = rows
		v.mu.Unlock()

		fyne.Do(func() {
			v.content.Segments = segs
			v.content.Refresh()
			v.status.SetText(fmt.Sprintf("%s / %s · %s", h.Device, h.Type, h.File))
		})
		v.mu.Lock()
		v.pendingScroll = h.Line
		v.mu.Unlock()
		v.loop.mark()
	}()
}

func artifactKey(h storesearch.Hit) string { return h.Device + "|" + h.Type + "|" + h.File }

// scrollToLine puts a 1-based line near the top of the content pane.
//
// Must be called on the main thread; every caller already is.
func (v *SearchView) scrollToLine(line int) {
	v.mu.RLock()
	rows := v.contentRows
	v.mu.RUnlock()
	if line < 1 || rows <= 0 {
		return
	}

	// The row height comes from the widget's own laid-out height rather
	// than from re-measuring a glyph. Padding is included and spread across
	// every row, so the error is a fraction of a pixel per row and never
	// reaches a whole one.
	rowHeight := v.content.MinSize().Height / float32(rows)

	y := float32(line-1)*rowHeight - v.scroll.Size().Height/4
	if y < 0 {
		y = 0
	}
	// ScrollToOffset rather than assigning Offset: it clamps to the
	// content, so a hit on the last line of a short file does not scroll
	// past the end and render blank.
	v.scroll.ScrollToOffset(fyne.NewPos(0, y))
}

// configSegments turns an artifact into one RichText row per line, with every
// occurrence of the needle styled.
//
// Matches are re-found here rather than being handed in, because a Hit carries
// a trimmed and possibly truncated line while highlighting has to work on the
// real one. The fold flag comes from the matcher, so the highlighter and the
// matcher cannot disagree about what counted as a match.
func configSegments(text, needle string, fold bool) (segs []widget.RichTextSegment, rows int) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	plain := widget.RichTextStyle{TextStyle: fyne.TextStyle{Monospace: true}}
	// RichTextStyle carries no background colour, so a match is marked by
	// colour and weight rather than by a block behind it. Bold as well as
	// coloured, because colour alone is not a signal everyone can see.
	hit := widget.RichTextStyle{
		TextStyle: fyne.TextStyle{Monospace: true, Bold: true},
		ColorName: theme.ColorNamePrimary,
	}

	segs = make([]widget.RichTextSegment, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			// An empty segment collapses and the row disappears,
			// which slips every line number below it — and the
			// line number is how this pane is navigated.
			line = " "
		}

		// Inline reads the opposite way round to the obvious guess: a row
		// breaks AFTER a segment whose Inline is false, so it terminates
		// a row rather than starting one. Every segment of a line is
		// therefore Inline EXCEPT the last. Getting this backwards puts
		// the tail of one line at the head of the next and silently
		// shifts every line number below it.
		lineStart := len(segs)
		emit := func(part string, style widget.RichTextStyle) {
			style.Inline = true
			segs = append(segs, &widget.TextSegment{Text: part, Style: style})
		}
		endLine := func() {
			if len(segs) > lineStart {
				last := segs[len(segs)-1].(*widget.TextSegment)
				last.Style.Inline = false
			}
		}

		hay := line
		if fold {
			hay = strings.ToLower(line)
		}
		// Folding can change byte length outside ASCII, and then an
		// index into the lowered string does not name the same bytes
		// in the original. Rather than slice wrongly, that line simply
		// goes unhighlighted — it is still on screen and still found.
		if needle == "" || len(hay) != len(line) {
			emit(line, plain)
			endLine()
			continue
		}

		from := 0
		for {
			i := strings.Index(hay[from:], needle)
			if i < 0 {
				break
			}
			start := from + i
			if start > from {
				emit(line[from:start], plain)
			}
			emit(line[start:start+len(needle)], hit)
			from = start + len(needle)
		}
		if from < len(line) {
			emit(line[from:], plain)
		}
		endLine()
	}
	return segs, len(lines)
}
