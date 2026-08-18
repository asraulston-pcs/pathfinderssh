// internal/ui/mapdialog.go
//
// The map picker: choose a folder, see the maps in it, open one.
//
// It is list-and-open and nothing else. There is no rename, no delete and no
// archive, because these are files in a folder the operator already owns and a
// file manager is better at managing them than a dialog embedded in a terminal
// application would ever be.
//
// Like the other dialogs here it is data in, data out: it cannot start a
// server, open a browser or read a map. It hands back which file was chosen
// and the host does the rest.
package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/mapweb"
)

// MapLaunch is what the picker produces: the file to open, and the folder it
// came from so the host can offer the same one next time.
type MapLaunch struct {
	Dir  string
	File mapweb.MapFile
}

// ShowMapDialog opens the picker on dir. onOpen is called with the chosen
// file; cancelling calls nothing.
func ShowMapDialog(w fyne.Window, dir string, onOpen func(MapLaunch)) {
	var files []mapweb.MapFile
	selected := -1

	folder := widget.NewEntry()
	folder.SetPlaceHolder("folder holding your map.json files, e.g. ~/pf_maps")
	folder.SetText(dir)

	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord

	list := widget.NewList(
		func() int { return len(files) },
		func() fyne.CanvasObject {
			// Name on the left at its natural width, summary filling the
			// rest. Built once and reused for every row — a List's item
			// objects are recycled, so nothing here may depend on which
			// row it last drew.
			return container.NewBorder(nil, nil, widget.NewLabel(""), nil, widget.NewLabel(""))
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < 0 || i >= len(files) {
				return
			}
			row, ok := o.(*fyne.Container)
			if !ok || len(row.Objects) < 2 {
				return
			}
			// container.NewBorder puts the centre object first and the
			// border objects after it, in the order they were passed.
			summary, _ := row.Objects[0].(*widget.Label)
			name, _ := row.Objects[1].(*widget.Label)
			if name == nil || summary == nil {
				return
			}

			f := files[i]
			name.TextStyle = fyne.TextStyle{Monospace: true}
			name.SetText(f.Name)
			summary.SetText(f.Summary())
		},
	)
	list.OnSelected = func(i widget.ListItemID) { selected = int(i) }
	list.OnUnselected = func(widget.ListItemID) { selected = -1 }

	refresh := func() {
		selected = -1
		list.UnselectAll()

		found, err := mapweb.ListMaps(ExpandHome(folder.Text))
		if err != nil {
			files = nil
			status.SetText(err.Error())
			list.Refresh()
			return
		}

		files = found
		switch {
		case len(files) == 0:
			status.SetText("No .json files in that folder. A crawl writes one wherever its Map output points.")
		case len(files) == 1:
			status.SetText("1 map")
		default:
			status.SetText(fmt.Sprintf("%d maps, newest first", len(files)))
		}
		list.Refresh()
	}

	reload := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), refresh)
	reload.Importance = widget.LowImportance

	// Browsing to a folder should list it immediately rather than leave the
	// person to press a second button for something they already chose.
	folder.OnChanged = func(string) { refresh() }

	top := container.NewBorder(nil, nil, widget.NewLabel("Folder"), reload,
		pathRow(w, folder, pathFolder, ""))
	content := container.NewBorder(top, status, nil, nil, list)

	refresh()

	var show func()
	show = func() {
		d := dialog.NewCustomConfirm("Open map", "Open", "Cancel", content, func(ok bool) {
			if !ok {
				return
			}
			if selected < 0 || selected >= len(files) {
				status.SetText("Select a map to open.")
				show()
				return
			}
			f := files[selected]
			if !f.OK() {
				// A confirm dialog cannot refuse to dismiss, so say why
				// and re-open the same content, which still holds the
				// folder and the listing.
				status.SetText(f.Name + ": " + f.Problem)
				show()
				return
			}
			onOpen(MapLaunch{Dir: ExpandHome(folder.Text), File: f})
		}, w)
		d.Resize(fyne.NewSize(620, 460))
		d.Show()
		EnterConfirms(w, content, d.Confirm)
	}
	show()
}
