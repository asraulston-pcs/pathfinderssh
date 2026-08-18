// internal/ui/searchform.go
//
// The search launch dialog.
//
// Data in, data out, same contract as the crawl and capture dialogs: it cannot
// open a store, cannot run a search and does not own a window. It collects a
// SearchLaunch and hands it back.
//
// It is a new file rather than another function in launchforms.go because the
// two crawl/capture dialogs there are three tabs each and this one is a single
// page — and because a file that already holds two 200-line dialogs is where
// the third one goes to make all three harder to read.
package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/storesearch"
)

// SearchLaunch is what the dialog collects.
type SearchLaunch struct {
	Query     string
	StorePath string

	// Types empty means every capture type the store holds.
	Types         []string
	CaseSensitive bool

	// Limit caps hits held in memory. Zero takes the engine default.
	Limit int
}

// ShowSearchDialog collects a search.
//
// knownTypes comes from the caller for the same reason it does in the capture
// dialog: this package must not import the capture engine to find out what a
// capture type is. An empty list leaves the field free text, which still works.
func ShowSearchDialog(w fyne.Window, prev SearchLaunch, knownTypes []string, onRun func(SearchLaunch)) {
	query := entryWith(prev.Query)
	query.SetPlaceHolder("text to find, e.g. a hostname, an address, an interface")

	store := entryWith(prev.StorePath)
	store.SetPlaceHolder("capture store folder")

	// Case-sensitive is the opt-in. Configuration text mixes cases for one
	// token constantly, so a case-sensitive default produces confident
	// empty results — the worst answer a search can give.
	caseSensitive := widget.NewCheck("Case sensitive", nil)
	caseSensitive.SetChecked(prev.CaseSensitive)

	types := entryWith(strings.Join(prev.Types, ", "))
	typeChoice := widget.NewCheckGroup(knownTypes, nil)
	typeChoice.SetSelected(intersect(prev.Types, knownTypes))
	var typesField fyne.CanvasObject = types
	if len(knownTypes) > 0 {
		types.SetPlaceHolder(strings.Join(knownTypes, ", "))
		typesField = typeChoice
	}

	// A plain form container, not formOf: formOf wraps its rows in a
	// VScroll, which is right for a tall tabbed dialog and wrong inside a
	// VBox, where it collapses to a small height and shows one row with a
	// scrollbar. That has now happened twice in this package, so treat
	// formOf-inside-a-VBox as a defect pattern rather than a style choice.
	body := form(
		[2]fyne.CanvasObject{widget.NewLabel("Find"), query},
		[2]fyne.CanvasObject{widget.NewLabel("Store"), pathRow(w, store, pathFolder, "")},
		[2]fyne.CanvasObject{widget.NewLabel("Types"), typesField},
		[2]fyne.CanvasObject{widget.NewLabel(""), caseSensitive},
	)

	note := widget.NewLabel("Searches the current version of every capture. Matching is literal.")
	note.Wrapping = fyne.TextWrapWord

	content := container.NewVBox(body, note)

	var d dialog.Dialog

	// Return runs the search. The wiring is idempotent -- EnterConfirms
	// leaves an entry that already has a handler alone -- so calling this
	// again after the validation re-show only moves the focus, which is
	// the point: the same content object comes back and the cursor should
	// be in it.
	enterSearches := func() {
		EnterConfirms(w, content, func() {
			if c, ok := d.(*dialog.ConfirmDialog); ok {
				c.Confirm()
			}
		})
	}
	d = dialog.NewCustomConfirm("Search captures", "Search", "Cancel", content,
		func(ok bool) {
			if !ok {
				return
			}
			l := SearchLaunch{
				Query:         strings.TrimSpace(query.Text),
				StorePath:     ExpandHome(store.Text),
				CaseSensitive: caseSensitive.Checked,
				Limit:         prev.Limit,
			}
			if len(knownTypes) > 0 {
				l.Types = typeChoice.Selected
			} else {
				l.Types = splitTypeList(types.Text)
			}

			if problem := l.Validate(); problem != "" {
				// A confirm dialog cannot refuse to dismiss, so
				// re-open the SAME content object. Nothing
				// typed is lost, which is what makes a
				// validation failure survivable rather than
				// annoying.
				dialog.ShowError(errString(problem), w)
				d = dialog.NewCustomConfirm("Search captures", "Search", "Cancel", content,
					func(ok bool) {
						if ok {
							onRun(l)
						}
					}, w)
				d.Resize(searchDialogSize)
				d.Show()
				enterSearches()
				return
			}
			onRun(l)
		}, w)
	d.Resize(searchDialogSize)
	d.Show()
	enterSearches()
}

var searchDialogSize = fyne.NewSize(620, 380)

// Validate reports the first problem with a launch, or "".
//
// It checks shape only and never touches the filesystem — the same rule the
// crawl and capture Params validators follow, so that validation can be shared
// with a caller that has no store yet.
func (l SearchLaunch) Validate() string {
	if strings.TrimSpace(l.Query) == "" {
		return storesearch.ErrEmptyQuery.Error()
	}
	if strings.TrimSpace(l.StorePath) == "" {
		return "no store: search reads captures, so it needs the folder they were written to"
	}
	return ""
}

// splitTypeList parses a free-text type list on the separators people actually
// type. Same permissiveness as the device-list parsers, for the same reason.
// Named apart from vaultmodel's splitList, which splits on commas only.
func splitTypeList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == ';'
	}) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// errString turns a message into an error for dialog.ShowError without
// dragging errors into every call site.
type errString string

func (e errString) Error() string { return string(e) }
