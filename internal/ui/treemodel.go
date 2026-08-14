// internal/ui/treemodel.go
//
// What the session tree widget displays, as data.
//
// The widget is a fyne.Tree, which asks three questions over and over — what
// are this node's children, is it a branch, what does it say — and answers have
// to be cheap and consistent. Computing them from a sessions.Tree on every call
// would be both slow and subtly wrong, because a filtered view has to answer
// differently from an unfiltered one while the underlying inventory is
// unchanged.
//
// So the whole view is precomputed once per change into a flat map of rows plus
// a child index. That also puts the only interesting logic in this file —
// matching, and which folders survive a filter — where it can be compiled and
// tested without a display.
//
// # UIDs
//
// A fyne.Tree identifies rows by string. Folder rows are "f:<name>" and session
// rows are "s:<folder>/<label>", because a session label is only unique inside
// its folder — two sites both having a "core-1" is normal, and keying on the
// label alone would make the tree show one of them twice.
package ui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

// TreeRootUID is the invisible parent fyne.Tree asks about first.
const TreeRootUID = ""

// TreeRow is one visible line.
type TreeRow struct {
	UID      string
	IsFolder bool
	Folder   string // the folder this row is in, or is
	Label    string
	Detail   string // right-hand text: the target, for sessions
	Node     sessions.Node
}

// TreeView is a whole rendered tree: rows by UID, and each row's children.
type TreeView struct {
	Rows     map[string]TreeRow
	Children map[string][]string

	// Matched is how many sessions the filter let through. Zero with a
	// non-empty filter is the case the widget has to say something about,
	// rather than showing an empty panel that looks broken.
	Matched int
}

// FolderUID and SessionUID build the identifiers, in one place so the widget
// and the model cannot disagree about them.
func FolderUID(folder string) string { return "f:" + folder }

// SessionUID keys on folder AND label because a label is only unique within a
// folder.
func SessionUID(folder, label string) string { return "s:" + folder + "/" + label }

// BuildTreeView renders a session tree for display, applying a filter.
//
// Filtering keeps a folder when the folder's own name matches — the whole
// folder then shows, which is what someone typing a site code wants — or when
// any session in it matches, in which case only the matching sessions show.
// A folder with no matches is dropped entirely rather than shown empty, since
// an empty branch in a filtered tree is a row that costs a click to learn
// nothing.
//
// An empty filter shows everything, including empty folders: a folder someone
// just made and has not filled yet must not vanish.
func BuildTreeView(t sessions.Tree, filter string) TreeView {
	q := strings.ToLower(strings.TrimSpace(filter))

	v := TreeView{
		Rows:     map[string]TreeRow{},
		Children: map[string][]string{},
	}

	for _, f := range t.Folders {
		folderHit := q != "" && strings.Contains(strings.ToLower(f.Name), q)

		kids := make([]string, 0, len(f.Sessions))
		for _, n := range f.Sessions {
			if q != "" && !folderHit && !MatchesSession(n, q) {
				continue
			}
			label := n.Label()
			uid := SessionUID(f.Name, label)
			// A duplicate label inside one folder should be impossible — Add
			// and Replace both refuse it — but a hand-edited file can contain
			// anything, and a repeated UID makes fyne.Tree render one row and
			// silently lose the other. Suffix rather than drop.
			for _, taken := v.Rows[uid]; taken; _, taken = v.Rows[uid] {
				label += " "
				uid = SessionUID(f.Name, label)
			}
			v.Rows[uid] = TreeRow{
				UID:    uid,
				Folder: f.Name,
				Label:  label,
				Detail: n.Target(),
				Node:   n,
			}
			kids = append(kids, uid)
			v.Matched++
		}

		if q != "" && !folderHit && len(kids) == 0 {
			continue
		}

		fuid := FolderUID(f.Name)
		v.Rows[fuid] = TreeRow{
			UID:      fuid,
			IsFolder: true,
			Folder:   f.Name,
			Label:    f.Name,
			Detail:   folderDetail(len(kids)),
		}
		v.Children[fuid] = kids
		v.Children[TreeRootUID] = append(v.Children[TreeRootUID], fuid)
	}

	return v
}

func folderDetail(n int) string {
	if n == 1 {
		return "1 session"
	}
	// strconv rather than a local helper: package ui already has an unexported
	// itoa in sessionform.go, and a second one is a redeclaration.
	return strconv.Itoa(n) + " sessions"
}

// IsBranch answers fyne.Tree's second question, and it has one rule that is
// not obvious from the data: THE ROOT IS ALWAYS A BRANCH.
//
// fyne.Tree walks from Root (the empty UID by default) and descends only if
// IsBranch says that node is a branch. Looking the root up in Rows finds
// nothing, returns the zero value, and the whole tree renders as a single
// invisible leaf — no folders, no sessions, no error, on every tree including
// a correct one. Answering it here rather than in the widget keeps the rule
// testable.
func (v TreeView) IsBranch(uid string) bool {
	if uid == TreeRootUID {
		return true
	}
	return v.Rows[uid].IsFolder
}

// MatchesSession reports whether a node matches a lowercased query.
//
// The fields searched are the ones somebody would type looking for a box: what
// it is called, where it is, and what it is. Credential names, key paths and
// theme names are deliberately not searched — matching a device because its
// key file has "core" in the path is a result nobody can explain.
func MatchesSession(n sessions.Node, q string) bool {
	if q == "" {
		return true
	}
	for _, field := range []string{
		n.Name,
		n.Host,
		n.SerialPort,
		n.DeviceType,
		n.Vendor,
		n.Model,
		n.Username,
		n.Notes,
	} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// FolderNames lists the folders in file order, for a picker that has to offer
// somewhere to put a session.
func FolderNames(t sessions.Tree) []string {
	out := make([]string, 0, len(t.Folders))
	for _, f := range t.Folders {
		out = append(out, f.Name)
	}
	return out
}

// SortedFolderNames is the same list alphabetically, for a menu long enough
// that file order stops being findable.
func SortedFolderNames(t sessions.Tree) []string {
	out := FolderNames(t)
	sort.Strings(out)
	return out
}

// ExpandedFor is which folders the widget should open.
//
// With no filter, nothing is forced open — the tree remembers what the person
// opened. With a filter, every surviving folder opens, because a filtered tree
// whose branches are shut shows a list of folder names and none of the matches
// that were just searched for.
func ExpandedFor(v TreeView, filter string) []string {
	if strings.TrimSpace(filter) == "" {
		return nil
	}
	out := make([]string, 0, len(v.Children[TreeRootUID]))
	out = append(out, v.Children[TreeRootUID]...)
	return out
}
