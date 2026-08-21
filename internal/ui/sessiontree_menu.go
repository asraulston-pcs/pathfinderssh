// internal/ui/sessiontree_menu.go
//
// The right-click menu on a tree row.
//
// Everything here is a second route to something the panel could already do,
// plus the three operations the model gained for it: reorder, duplicate, and a
// Move to Folder that the model has always had and nothing ever called. The
// four-button action bar stays as it is — it is discoverable and this is not.
//
// # Why a menu and not more buttons
//
// The panel is a quarter of the window wide and already spends a row on four
// icons. Seven more operations cannot go there. A context menu costs no width,
// and every one of these actions is about a specific row, which is exactly the
// thing a right-click already identifies.
//
// # Reorder is hidden while a filter is on
//
// A filtered tree shows a subset in its true relative order, so Move Up still
// moves a session up — but if the row above it is filtered out, the row does
// not appear to move. The operation would be correct and would look broken.
// Rather than explain that, the two entries are absent while the search box has
// text in it; clearing the filter brings them back.
//
// # Reselection
//
// The row UID is "s:<folder>/<label>", which does not encode position, so after
// a reorder the same UID still names the same session and reselecting it is
// just Select(uid). Duplicate and Move both change one half of that key, so
// they compute the new UID from what the model handed back rather than assuming
// the old one still resolves.
package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

// showRowMenu opens the context menu for a row at an absolute screen position.
func (t *SessionTree) showRowMenu(uid widget.TreeNodeID, pos fyne.Position) {
	if t.opts.Window == nil {
		return
	}
	row, ok := t.view.Rows[uid]
	if !ok {
		return
	}
	// Select first, so the row the menu acts on is the row highlighted behind
	// it — the same reason rowEdit selects before opening the form.
	t.rowTapped(uid)

	var menu *fyne.Menu
	if row.IsFolder {
		menu = t.folderMenu(row)
	} else {
		menu = t.sessionMenu(row)
	}
	widget.ShowPopUpMenuAtPosition(menu, t.opts.Window.Canvas(), pos)
}

// filtered reports whether the search box is narrowing the tree, which is what
// makes a reorder look like it did nothing.
func (t *SessionTree) filtered() bool {
	return t.search != nil && t.search.Text != ""
}

func (t *SessionTree) sessionMenu(row TreeRow) *fyne.Menu {
	folder, label := row.Folder, row.Label

	open := &fyne.MenuItem{
		Label:  "Open",
		Icon:   theme.MediaPlayIcon(),
		Action: func() { t.rowActivated(row.UID) },
	}
	edit := &fyne.MenuItem{
		Label:  "Edit",
		Icon:   theme.DocumentCreateIcon(),
		Action: func() { t.editRow(row.UID) },
	}
	duplicate := &fyne.MenuItem{
		Label:  "Duplicate",
		Icon:   theme.ContentCopyIcon(),
		Action: func() { t.duplicateSession(folder, label) },
	}
	move := &fyne.MenuItem{
		Label:  "Move to Folder…",
		Icon:   theme.FolderOpenIcon(),
		Action: func() { t.moveSessionDialog(folder, label) },
	}
	del := &fyne.MenuItem{
		Label:  "Delete",
		Icon:   theme.DeleteIcon(),
		Action: func() { t.deleteSession(folder, label) },
	}

	items := []*fyne.MenuItem{open, edit, duplicate, move}
	if !t.filtered() {
		items = append(items,
			&fyne.MenuItem{
				Label:  "Move Up",
				Icon:   theme.MoveUpIcon(),
				Action: func() { t.reorderSession(folder, label, -1) },
			},
			&fyne.MenuItem{
				Label:  "Move Down",
				Icon:   theme.MoveDownIcon(),
				Action: func() { t.reorderSession(folder, label, +1) },
			},
		)
	}
	items = append(items, fyne.NewMenuItemSeparator(), del)
	return fyne.NewMenu("", items...)
}

func (t *SessionTree) folderMenu(row TreeRow) *fyne.Menu {
	name := row.Folder

	items := []*fyne.MenuItem{
		{
			Label:  "New Session Here",
			Icon:   theme.ContentAddIcon(),
			Action: func() { t.newSessionIn(name) },
		},
		{
			Label:  "New Folder",
			Icon:   theme.FolderNewIcon(),
			Action: t.newFolder,
		},
		{
			Label:  "Rename Folder",
			Icon:   theme.DocumentCreateIcon(),
			Action: func() { t.renameFolder(name) },
		},
	}
	if !t.filtered() {
		items = append(items,
			&fyne.MenuItem{
				Label:  "Move Up",
				Icon:   theme.MoveUpIcon(),
				Action: func() { t.reorderFolder(name, -1) },
			},
			&fyne.MenuItem{
				Label:  "Move Down",
				Icon:   theme.MoveDownIcon(),
				Action: func() { t.reorderFolder(name, +1) },
			},
		)
	}
	items = append(items, fyne.NewMenuItemSeparator(), &fyne.MenuItem{
		Label:  "Delete Folder",
		Icon:   theme.DeleteIcon(),
		Action: func() { t.deleteFolder(row) },
	})
	return fyne.NewMenu("", items...)
}

// ── operations ───────────────────────────────────────────────────────

// newSessionIn is newSession with the folder named rather than inferred from
// the selection, which is what "New Session Here" promises.
func (t *SessionTree) newSessionIn(folder string) {
	if t.opts.OnNew == nil || folder == "" {
		return
	}
	t.opts.OnNew(folder, func(n sessions.Node) {
		if err := t.tree.Add(folder, n); err != nil {
			t.error(err)
			return
		}
		t.changed()
		t.reveal(folder, SessionUID(folder, n.Label()))
	})
}

func (t *SessionTree) duplicateSession(folder, label string) {
	dup, err := t.tree.Duplicate(folder, label)
	if err != nil {
		t.error(err)
		return
	}
	t.changed()
	t.reveal(folder, SessionUID(folder, dup.Label()))
}

func (t *SessionTree) reorderSession(folder, label string, delta int) {
	if err := t.tree.ReorderSession(folder, label, delta); err != nil {
		t.error(err)
		return
	}
	t.changed()
	// The UID is folder plus label and a reorder changes neither, so the row
	// that was selected is still the row to select.
	t.reveal(folder, SessionUID(folder, label))
}

func (t *SessionTree) reorderFolder(name string, delta int) {
	if err := t.tree.ReorderFolder(name, delta); err != nil {
		t.error(err)
		return
	}
	t.changed()
	if t.tw != nil {
		t.tw.Select(FolderUID(name))
	}
}

func (t *SessionTree) deleteSession(folder, label string) {
	dialog.ShowConfirm("Delete session",
		fmt.Sprintf("Remove %q from %q?", label, folder),
		func(yes bool) {
			if !yes {
				return
			}
			if err := t.tree.Remove(folder, label); err != nil {
				t.error(err)
				return
			}
			t.changed()
		}, t.opts.Window)
}

// deleteFolder is deleteSelected's folder branch, reached from the menu rather
// than from the action bar. The count comes from the rendered view, so a
// filtered tree would undercount it — which is the other reason the question
// names the folder rather than only the number.
func (t *SessionTree) deleteFolder(row TreeRow) {
	count := len(t.view.Children[row.UID])
	msg := fmt.Sprintf("Remove the folder %q?", row.Folder)
	if count > 0 {
		msg = fmt.Sprintf("Remove %q and the %d session(s) in it?", row.Folder, count)
	}
	dialog.ShowConfirm("Delete folder", msg, func(yes bool) {
		if !yes {
			return
		}
		if err := t.tree.RemoveFolder(row.Folder, true); err != nil {
			t.error(err)
			return
		}
		t.changed()
	}, t.opts.Window)
}

// moveSessionDialog asks which folder to move a session to.
//
// The list excludes the folder the session is already in — offering it would be
// offering a no-op — and the dialog is refused outright when that leaves
// nothing, because a picker with an empty list is a dead end that still costs
// two clicks to escape.
func (t *SessionTree) moveSessionDialog(folder, label string) {
	options := make([]string, 0, len(t.tree.Folders))
	for _, name := range FolderNames(t.tree) {
		if name != folder {
			options = append(options, name)
		}
	}
	if len(options) == 0 {
		t.error(fmt.Errorf("there is nowhere else to move it — make another folder first"))
		return
	}

	sel := widget.NewSelect(options, nil)
	sel.SetSelectedIndex(0)

	items := []*widget.FormItem{widget.NewFormItem("Folder", sel)}
	d := dialog.NewForm("Move to folder", "Move", "Cancel", items,
		func(ok bool) {
			if !ok || sel.Selected == "" {
				return
			}
			t.moveSession(folder, label, sel.Selected)
		}, t.opts.Window)
	d.Show()
	EnterConfirmsForm(t.opts.Window, items, d.Submit)
}

// moveSession is the move itself, separated from the picker that chose the
// destination — the dialog is one caller, and a drag-and-drop or a keyboard
// shortcut would be another.
func (t *SessionTree) moveSession(folder, label, target string) {
	if target == "" || target == folder {
		return
	}
	if err := t.tree.Move(folder, label, target); err != nil {
		t.error(err)
		return
	}
	t.changed()
	// The folder half of the UID changed, so the old one no longer resolves.
	// Open the destination first or the row is selected inside a shut branch
	// and nothing visible happens.
	t.reveal(target, SessionUID(target, label))
}

// reveal opens a folder and selects a row inside it, in that order.
func (t *SessionTree) reveal(folder, uid string) {
	if t.tw == nil {
		return
	}
	t.tw.OpenBranch(FolderUID(folder))
	if _, ok := t.view.Rows[uid]; !ok {
		return
	}
	t.tw.Select(uid)
}
