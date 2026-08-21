// internal/ui/sessiontree_menu_test.go
//
// Driven headless through fyne's test driver: the menu is built from a real
// SessionTree over a real sessions.Tree, and the entries are invoked the way a
// click would invoke them. What is being checked is the wiring — that an entry
// reaches the model operation it names, and that the panel is still coherent
// afterwards — not the drawing.
package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

func menuTestTree(t *testing.T) (*SessionTree, fyne.Window, *int) {
	t.Helper()

	tr := sessions.Tree{}
	if err := tr.Add("Lab", sessions.Node{Name: "eng-leaf-1", Transport: sessions.TransportSSH, Host: "172.16.2.11"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Add("Lab", sessions.Node{Name: "eng-leaf-2", Transport: sessions.TransportSSH, Host: "172.16.2.12"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Add("Core", sessions.Node{Name: "wan-core-1", Transport: sessions.TransportSSH, Host: "172.16.1.2"}); err != nil {
		t.Fatal(err)
	}

	saves := 0
	w := test.NewWindow(nil)
	st := NewSessionTree(SessionTreeOptions{
		Window:    w,
		OnChanged: func(sessions.Tree) { saves++ },
	})
	st.SetTree(tr)
	w.SetContent(st.Content())
	return st, w, &saves
}

// labels flattens a menu to its entry labels, separators included as "-".
func labels(m *fyne.Menu) []string {
	out := make([]string, 0, len(m.Items))
	for _, it := range m.Items {
		if it.IsSeparator {
			out = append(out, "-")
			continue
		}
		out = append(out, it.Label)
	}
	return out
}

func invoke(t *testing.T, m *fyne.Menu, label string) {
	t.Helper()
	for _, it := range m.Items {
		if it.Label == label {
			if it.Action == nil {
				t.Fatalf("menu entry %q has no action", label)
			}
			it.Action()
			return
		}
	}
	t.Fatalf("no menu entry called %q in %v", label, labels(m))
}

func sessionLabels(st *SessionTree, folder string) []string {
	tr := st.Tree()
	i := tr.FolderIndex(folder)
	if i < 0 {
		return nil
	}
	out := make([]string, 0, len(tr.Folders[i].Sessions))
	for _, n := range tr.Folders[i].Sessions {
		out = append(out, n.Label())
	}
	return out
}

func folderLabels(st *SessionTree) []string {
	tr := st.Tree()
	out := make([]string, 0, len(tr.Folders))
	for _, f := range tr.Folders {
		out = append(out, f.Name)
	}
	return out
}

func joined(s []string) string { return strings.Join(s, ",") }

// ── shape ────────────────────────────────────────────────────────────

func TestSessionMenuHasTheExpectedEntries(t *testing.T) {
	st, w, _ := menuTestTree(t)
	defer w.Close()

	row := st.view.Rows[SessionUID("Lab", "eng-leaf-1")]
	got := joined(labels(st.sessionMenu(row)))
	want := "Open,Edit,Duplicate,Move to Folder…,Move Up,Move Down,-,Delete"
	if got != want {
		t.Fatalf("session menu is %q, want %q", got, want)
	}
}

func TestFolderMenuHasTheExpectedEntries(t *testing.T) {
	st, w, _ := menuTestTree(t)
	defer w.Close()

	row := st.view.Rows[FolderUID("Lab")]
	got := joined(labels(st.folderMenu(row)))
	want := "New Session Here,New Folder,Rename Folder,Move Up,Move Down,-,Delete Folder"
	if got != want {
		t.Fatalf("folder menu is %q, want %q", got, want)
	}
}

// A filtered tree shows a subset in true relative order, so a reorder would be
// correct and would look like nothing happened. The entries go away instead.
func TestReorderEntriesHiddenWhileFiltering(t *testing.T) {
	st, w, _ := menuTestTree(t)
	defer w.Close()

	st.search.SetText("leaf")
	st.refresh()

	row := st.view.Rows[SessionUID("Lab", "eng-leaf-1")]
	got := joined(labels(st.sessionMenu(row)))
	if strings.Contains(got, "Move Up") || strings.Contains(got, "Move Down") {
		t.Fatalf("reorder offered while filtering: %q", got)
	}
	if !strings.Contains(got, "Duplicate") {
		t.Fatalf("filtering removed more than it should: %q", got)
	}

	st.search.SetText("")
	st.refresh()
	row = st.view.Rows[SessionUID("Lab", "eng-leaf-1")]
	if !strings.Contains(joined(labels(st.sessionMenu(row))), "Move Up") {
		t.Fatal("reorder did not come back when the filter was cleared")
	}
}

// ── wiring ───────────────────────────────────────────────────────────

func TestMenuReorderSessionMovesAndSaves(t *testing.T) {
	st, w, saves := menuTestTree(t)
	defer w.Close()

	row := st.view.Rows[SessionUID("Lab", "eng-leaf-2")]
	invoke(t, st.sessionMenu(row), "Move Up")

	if got := joined(sessionLabels(st, "Lab")); got != "eng-leaf-2,eng-leaf-1" {
		t.Fatalf("order is %q", got)
	}
	if *saves != 1 {
		t.Fatalf("host was told to save %d times, want 1", *saves)
	}
	// The UID does not encode position, so the moved row is still selected.
	if st.selected != SessionUID("Lab", "eng-leaf-2") {
		t.Fatalf("selection after reorder is %q", st.selected)
	}
}

func TestMenuReorderFolderMoves(t *testing.T) {
	st, w, _ := menuTestTree(t)
	defer w.Close()

	row := st.view.Rows[FolderUID("Core")]
	invoke(t, st.folderMenu(row), "Move Up")

	if got := joined(folderLabels(st)); got != "Core,Lab" {
		t.Fatalf("folder order is %q", got)
	}
}

func TestMenuDuplicateInsertsAfterAndSelectsTheCopy(t *testing.T) {
	st, w, saves := menuTestTree(t)
	defer w.Close()

	row := st.view.Rows[SessionUID("Lab", "eng-leaf-1")]
	invoke(t, st.sessionMenu(row), "Duplicate")

	if got := joined(sessionLabels(st, "Lab")); got != "eng-leaf-1,eng-leaf-1 copy,eng-leaf-2" {
		t.Fatalf("order after duplicate is %q", got)
	}
	if *saves != 1 {
		t.Fatalf("host was told to save %d times, want 1", *saves)
	}
	if st.selected != SessionUID("Lab", "eng-leaf-1 copy") {
		t.Fatalf("selection after duplicate is %q", st.selected)
	}
	// The copy has to be a row the tree can actually draw, not just an entry
	// in the model.
	if _, ok := st.view.Rows[SessionUID("Lab", "eng-leaf-1 copy")]; !ok {
		t.Fatal("the copy is not in the rendered view")
	}
}

func TestMenuDeleteSessionAsksFirst(t *testing.T) {
	st, w, saves := menuTestTree(t)
	defer w.Close()

	row := st.view.Rows[SessionUID("Lab", "eng-leaf-1")]
	invoke(t, st.sessionMenu(row), "Delete")

	// The confirmation is up; nothing has happened yet.
	if got := joined(sessionLabels(st, "Lab")); got != "eng-leaf-1,eng-leaf-2" {
		t.Fatalf("delete acted before the confirmation was answered: %q", got)
	}
	if *saves != 0 {
		t.Fatalf("host saved before confirmation, %d times", *saves)
	}
}

// Move to Folder is the operation the model has always had and nothing called.
func TestMenuMoveToFolder(t *testing.T) {
	st, w, _ := menuTestTree(t)
	defer w.Close()

	// The dialog picks the first other folder by default, which here is Core.
	st.moveSession("Lab", "eng-leaf-1", "Core")

	if got := joined(sessionLabels(st, "Lab")); got != "eng-leaf-2" {
		t.Fatalf("source folder is %q", got)
	}
	if got := joined(sessionLabels(st, "Core")); got != "wan-core-1,eng-leaf-1" {
		t.Fatalf("destination folder is %q", got)
	}
}

// A single-folder tree has nowhere to move to; the picker must not open empty.
func TestMoveToFolderRefusedWithNoDestination(t *testing.T) {
	tr := sessions.Tree{}
	if err := tr.Add("Lab", sessions.Node{Name: "eng-leaf-1", Transport: sessions.TransportSSH, Host: "172.16.2.11"}); err != nil {
		t.Fatal(err)
	}
	w := test.NewWindow(nil)
	defer w.Close()
	st := NewSessionTree(SessionTreeOptions{Window: w})
	st.SetTree(tr)
	w.SetContent(st.Content())

	st.moveSessionDialog("Lab", "eng-leaf-1")

	if got := joined(sessionLabels(st, "Lab")); got != "eng-leaf-1" {
		t.Fatalf("session moved when there was nowhere to move it: %q", got)
	}
}

// ── the secondary tap itself ─────────────────────────────────────────

// The row has to answer the right button at all, and selecting on the way is
// what makes the menu act on the row under the cursor rather than the row that
// happened to be selected before.
func TestSecondaryTapSelectsTheRow(t *testing.T) {
	st, w, _ := menuTestTree(t)
	defer w.Close()

	row := newSessionRow(st)
	uid := SessionUID("Lab", "eng-leaf-2")
	row.set(uid, st.view.Rows[uid])
	row.TappedSecondary(&fyne.PointEvent{})

	if st.selected != uid {
		t.Fatalf("secondary tap left the selection at %q", st.selected)
	}
}
