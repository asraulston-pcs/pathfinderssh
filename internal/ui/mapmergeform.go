// internal/ui/mapmergeform.go
//
// The map merge dialog, and the report it produces.
//
// A crawl stops where it cannot go, so an estate behind two walls is two maps.
// topo.Merge folds them; this is the form that collects which files, how hard
// to look for the same device in both, and where the result goes. It follows
// the crawl and capture dialogs deliberately — same pathRow, same formOf, same
// status label, same "collect a struct and hand it to a callback" shape — so a
// fourth dialog is a fourth of the same thing rather than a new idea.
//
// # Why the report is a second dialog and not a status line
//
// The interesting output of a merge is not "done". It is the list of nodes it
// decided were the same device, and the shorter list it refused to decide
// about. Both are judgments the person has to be able to disagree with, and
// neither fits on one line. A merge that only reported a node count would be
// asking to be trusted about exactly the part that is guesswork.
//
// # The options, in the order they are worth worrying about
//
// Domains is free and always right when both crawls used the same list.
// Match on management IP is strong. Match on short name is the one that reads
// as harmless and is not: core-1.site-a and core-1.site-b are two buildings.
// The form says so next to the checkbox rather than in documentation nobody
// opens, and topo.Merge refuses an ambiguous match regardless.
package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/topo"
)

// MapMergeLaunch is everything the merge dialog collects.
type MapMergeLaunch struct {
	// BasePath is the map whose names win. Order is a real decision here,
	// not a formality, which is why the field is labelled rather than the
	// two files being an unordered pair.
	BasePath string `json:"base_path,omitempty"`

	// IncomingPaths are folded in left to right after the base.
	IncomingPaths []string `json:"incoming_paths,omitempty"`

	// OutPath is where the merged map is written.
	OutPath string `json:"out_path,omitempty"`

	// Options are passed to topo.Merge unchanged.
	Options topo.MergeOptions `json:"options,omitempty"`

	// OpenAfter opens the result in the map viewer once it is written.
	OpenAfter bool `json:"open_after,omitempty"`
}

// Domains is the strip list as the form shows it: one line of separators
// rather than a list widget, since it is almost always one suffix.
func (l MapMergeLaunch) Domains() string {
	return strings.Join(l.Options.StripDomains, ", ")
}

// ShowMapMergeDialog collects a merge and calls onRun with it.
//
// prev seeds the fields the same way the crawl dialog's does, so merging a
// third map into yesterday's result is not a retype.
func ShowMapMergeDialog(w fyne.Window, prev MapMergeLaunch, onRun func(MapMergeLaunch)) {
	basePath := entryWith(prev.BasePath)
	basePath.SetPlaceHolder("map.json the merged names come from")

	// One path per line rather than a list widget. The number of maps is
	// two in every case this was built for and three in the bad week; a
	// list with add and remove buttons would be more chrome than the field
	// deserves, and a text area can be pasted into.
	incoming := multiline("One map.json per line")
	incoming.SetText(strings.Join(prev.IncomingPaths, "\n"))

	outPath := entryWith(prev.OutPath)
	outPath.SetPlaceHolder("where the merged map is written")

	domains := entryWith(prev.Domains())
	domains.SetPlaceHolder("lab.example, site1.lab.example")

	matchIP := widget.NewCheck("Same management IP is the same device", nil)
	matchIP.SetChecked(prev.Options.MatchIP)

	matchShort := widget.NewCheck("Same first label is the same device", nil)
	matchShort.SetChecked(prev.Options.MatchShortName)

	// The warning is next to the control it is about. A checkbox whose
	// danger is documented elsewhere is a checkbox that gets ticked.
	shortNote := widget.NewLabel(
		"Matches core-1 to core-1.lab.example without knowing the domain.\n" +
			"Leave off where a site label distinguishes devices —\n" +
			"core-1.site-a and core-1.site-b are two devices.")
	shortNote.Wrapping = fyne.TextWrapWord

	openAfter := widget.NewCheck("Open the merged map when it is written", nil)
	openAfter.SetChecked(prev.OpenAfter)

	status := statusLabel()

	// Clear the last complaint as soon as anything is edited.
	//
	// Without this the label is a message about fields as they were when
	// Merge was last pressed, sitting under fields that have since been
	// corrected — so a merge that is now valid still reads as refused, and
	// the only way to find out is to press Merge and see what happens.
	// Every input clears it, including the checkboxes: which of them the
	// message was about is not something the person should have to work out.
	clearStatus := func() { status.SetText("") }
	for _, e := range []*widget.Entry{basePath, incoming, outPath, domains} {
		e.OnChanged = func(string) { clearStatus() }
	}
	for _, c := range []*widget.Check{matchIP, matchShort, openAfter} {
		c.OnChanged = func(bool) { clearStatus() }
	}

	form := formOf(
		"Base map", pathRow(w, basePath, pathOpenFile, "map.json"),
		"Merge in", tall(pathRow(w, incoming, pathOpenFile, "map.json"), 90),
		"Save as", pathRow(w, outPath, pathOutput, "merged-map.json"),
		"Domains", domains,
		"Match on", container.NewVBox(matchIP, matchShort, shortNote),
		"", openAfter,
	)

	var d dialog.Dialog
	run := widget.NewButton("Merge", func() {
		l := MapMergeLaunch{
			BasePath:      strings.TrimSpace(basePath.Text),
			IncomingPaths: splitLines(incoming.Text),
			OutPath:       strings.TrimSpace(outPath.Text),
			OpenAfter:     openAfter.Checked,
			Options: topo.MergeOptions{
				StripDomains:   splitList(domains.Text),
				MatchIP:        matchIP.Checked,
				MatchShortName: matchShort.Checked,
			},
		}
		if err := l.Validate(); err != nil {
			status.SetText(err.Error())
			return
		}
		d.Hide()
		onRun(l)
	})
	run.Importance = widget.HighImportance

	content := container.NewBorder(nil, container.NewVBox(status, run), nil, nil, form)
	d = dialog.NewCustom("Merge topology maps", "Cancel", content, w)
	d.Resize(fyne.NewSize(720, 620))
	d.Show()
}

// Validate refuses the merges that cannot work, before any file is read.
//
// The one worth spelling out is a base that is also in the incoming list.
// Folding a map into itself is not harmful — every node matches itself and the
// result is the same map — but it is always a mistake, and reporting "merged 40
// nodes" for it would look like the merge worked.
func (l MapMergeLaunch) Validate() error {
	if l.BasePath == "" {
		return fmt.Errorf("pick a base map")
	}
	if len(l.IncomingPaths) == 0 {
		return fmt.Errorf("add at least one map to merge in")
	}
	if l.OutPath == "" {
		return fmt.Errorf("say where to save the merged map")
	}
	if err := checkInputPath("Base map", l.BasePath); err != nil {
		return err
	}
	base := cleanPath(l.BasePath)
	seen := map[string]bool{}
	for _, p := range l.IncomingPaths {
		if err := checkInputPath("Merge in", p); err != nil {
			return err
		}
		c := cleanPath(p)
		// Named separately from a repeat within the list. "Listed twice"
		// sent someone looking for a duplicate line in a box that had one
		// line in it, when what was wrong was the field above.
		if c == base {
			return fmt.Errorf("%s is the base map — it cannot also be merged in",
				filepath.Base(p))
		}
		if seen[c] {
			return fmt.Errorf("%s is in the merge list twice", filepath.Base(p))
		}
		seen[c] = true
	}
	return checkOutputPath("Save as", l.OutPath)
}

// ShowMergeReport puts the result in front of the person who asked for it.
//
// Selectable rather than a plain label: the aliases are the part worth pasting
// into a ticket or a message to whoever owns the estate.
func ShowMergeReport(w fyne.Window, outPath string, reports []topo.MergeReport) {
	body := widget.NewMultiLineEntry()
	body.SetText(FormatMergeReports(outPath, reports))
	body.Wrapping = fyne.TextWrapOff

	d := dialog.NewCustom("Merge complete", "Close", container.NewMax(body), w)
	d.Resize(fyne.NewSize(760, 560))
	d.Show()
}

// FormatMergeReports renders one or more merge reports as text.
//
// Separated from the dialog so it can be tested without a display, and so the
// same text can be logged when someone runs a merge and then wants to know
// what it did an hour later.
func FormatMergeReports(outPath string, reports []topo.MergeReport) string {
	var b strings.Builder

	if len(reports) == 0 {
		return "Nothing was merged."
	}

	last := reports[len(reports)-1]
	fmt.Fprintf(&b, "Wrote %s\n", outPath)
	fmt.Fprintf(&b, "%d nodes in the merged map.\n", last.Nodes)

	totalMerged, totalAdded, totalEdges := 0, 0, 0
	for _, r := range reports {
		totalMerged += r.Merged
		totalAdded += r.Added
		totalEdges += r.EdgesAdded
	}
	fmt.Fprintf(&b, "%d %s recognised as already present, %d added, %d new %s.\n",
		totalMerged, plural("node", totalMerged), totalAdded, totalEdges, plural("link", totalEdges))

	for i, r := range reports {
		if len(reports) > 1 {
			fmt.Fprintf(&b, "\n── map %d ──\n", i+2)
		}

		if len(r.Aliases) > 0 {
			b.WriteString("\nTreated as the same device:\n")
			for _, from := range sortedAliasKeys(r.Aliases) {
				fmt.Fprintf(&b, "  %s  →  %s\n", from, r.Aliases[from])
			}
		}

		if len(r.Ambiguous) > 0 {
			b.WriteString("\nAmbiguous — NOT merged, kept separate:\n")
			for _, name := range r.Ambiguous {
				fmt.Fprintf(&b, "  %s\n", name)
			}
			b.WriteString("  Each of these matched more than one node.\n")
			b.WriteString("  Check them by hand; a domain suffix usually settles it.\n")
		}

		if len(r.Conflicts) > 0 {
			b.WriteString("\nThe maps disagreed:\n")
			for _, c := range r.Conflicts {
				fmt.Fprintf(&b, "  %s %s: kept %s, discarded %s\n",
					c.Node, c.Field, c.Kept, c.Dropped)
			}
		}
	}

	return b.String()
}

// ── small helpers ────────────────────────────────────────────────────

// splitLines is the "one path per line" field's reader. Blank lines are
// skipped rather than becoming an empty path that fails validation with a
// message about a file called "".
func splitLines(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// cleanPath is only for comparing two typed paths for sameness. It is not a
// resolution — a symlink and its target still read as two files — but it
// catches the case this is for, which is the same path with a trailing slash
// or a "./" on the front.
func cleanPath(p string) string {
	if abs, err := filepath.Abs(strings.TrimSpace(p)); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(strings.TrimSpace(p))
}

func sortedAliasKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
