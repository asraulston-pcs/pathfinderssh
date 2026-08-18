// internal/ui/enterkey.go
//
// Return confirms a dialog.
//
// Fyne does not do this for you. A dialog's buttons are ordinary buttons, and
// nothing in the toolkit binds Return to the confirming one -- there is no
// default button, and `widget.Form` does not submit on Return either. The one
// place it happens anywhere in Fyne is the file-save dialog, which wires
// `Entry.OnSubmitted` by hand. So does this.
//
// # Why walk the content instead of listing the fields
//
// Every dialog in this package builds its own entries and would have to hand
// them over one by one. That works until somebody adds a field and forgets,
// and then one box in one dialog silently does not answer Return -- a
// difference nobody reports as a bug and everybody feels. Walking the content
// means a field added later is covered by having been added.
//
// # Multi-line entries get SHIFT+Return, and it is free
//
// Fyne already draws this distinction for us. `Entry.typedKeyReturn` inserts a
// newline on a multi-line entry unless Shift is held AND OnSubmitted is set,
// in which case it submits. So wiring the seed list costs nothing -- plain
// Return still adds a seed -- and it gives the crawl dialog, whose first field
// is multi-line, a way to start without reaching for the mouse.
//
// An entry that ALREADY has an OnSubmitted keeps it. Those exist because a
// field wanted its own behaviour, and that is more specific than this.
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// EnterConfirms makes Return in any single-line entry inside content run
// submit -- Shift+Return in a multi-line one -- and focuses the first entry so
// Return works before anything has been clicked.
//
// Call it AFTER the dialog's Show. Focus resolves through the canvas, and
// while a dialog is up that is the overlay's focus manager -- which does not
// exist until the overlay does. Called before Show, the wiring still lands and
// only the focus is lost, which is the quieter half of the same complaint.
//
// It returns how many entries were wired, which is what a test asserts on and
// what tells you a dialog was built out of something this walk does not know
// about.
func EnterConfirms(w fyne.Window, content fyne.CanvasObject, submit func()) int {
	entries := EntriesIn(content)
	n := 0
	for _, e := range entries {
		if e.OnSubmitted != nil {
			continue
		}
		e.OnSubmitted = func(string) { submit() }
		n++
	}
	if w != nil && len(entries) > 0 {
		w.Canvas().Focus(entries[0])
	}
	return n
}

// EnterConfirmsForm is EnterConfirms for a dialog built from form items --
// dialog.NewForm and dialog.ShowForm -- where the content object is the
// toolkit's and the caller only ever holds the items.
//
// Pair it with FormDialog.Submit rather than with the callback directly:
// Submit refuses while validation is failing, which is the same answer the
// disabled button gives, and Return must not be a way around a check the
// mouse cannot get around.
func EnterConfirmsForm(w fyne.Window, items []*widget.FormItem, submit func()) int {
	box := container.NewVBox()
	for _, it := range items {
		if it != nil && it.Widget != nil {
			box.Add(it.Widget)
		}
	}
	return EnterConfirms(w, box, submit)
}

// EntriesIn returns every entry inside obj, in layout order.
//
// The walk knows the containers this application actually builds dialogs out
// of. Anything else is a leaf as far as this is concerned, so an unknown
// wrapper costs the fields inside it and nothing more -- the failure is the
// behaviour we have today, not a panic.
func EntriesIn(obj fyne.CanvasObject) []*widget.Entry {
	var out []*widget.Entry
	walkEntries(obj, &out)
	return out
}

func walkEntries(obj fyne.CanvasObject, out *[]*widget.Entry) {
	switch o := obj.(type) {
	case nil:
		return

	// A password entry is an Entry with a different renderer, so it lands
	// here too -- which is the case that matters most, since typing a
	// password and pressing Return is the reflex this whole file exists
	// for.
	case *widget.Entry:
		*out = append(*out, o)
	case *widget.SelectEntry:
		*out = append(*out, &o.Entry)

	case *fyne.Container:
		for _, c := range o.Objects {
			walkEntries(c, out)
		}
	case *container.Scroll:
		walkEntries(o.Content, out)
	case *container.Split:
		walkEntries(o.Leading, out)
		walkEntries(o.Trailing, out)
	case *container.ThemeOverride:
		walkEntries(o.Content, out)
	case *container.AppTabs:
		for _, t := range o.Items {
			walkEntries(t.Content, out)
		}
	case *container.DocTabs:
		for _, t := range o.Items {
			walkEntries(t.Content, out)
		}
	case *widget.Form:
		for _, it := range o.Items {
			if it != nil {
				walkEntries(it.Widget, out)
			}
		}
	case *widget.Card:
		walkEntries(o.Content, out)
	case *widget.Accordion:
		for _, it := range o.Items {
			if it != nil {
				walkEntries(it.Detail, out)
			}
		}
	}
}
