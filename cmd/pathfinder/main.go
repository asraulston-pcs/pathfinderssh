// cmd/pathfinder/main.go
// The application shell: one window, three kinds of applet, tabs.
//
// This is the first thing in the tree that is an application rather than a
// harness. cmd/pfterm, cmd/pfconnect, cmd/crawlui and cmd/captureui stay --
// they are the fastest way to reproduce a bug in one view with the shell out of
// the way, and they mean the shell can be broken without breaking the three
// things that already work.
//
// What lives here and not in internal/ui: connecting. The shell hosts applets
// and knows nothing about dialers, vaults or crawlers; this file assembles
// them. That is the guard against TetherSSH's session manager, which reached
// 2,000 lines by becoming the place connections and dialogs both ended up.
//
//	go run ./cmd/pathfinder
//	go run ./cmd/pathfinder -vault ~/.pathfinderssh/vault.json -store ~/captures
//	go run ./cmd/pathfinder -vault ~/.pathfinderssh/vault.json -domain lab.local
//
// # What to check once it is up
//
//   - open a terminal, a crawl and a capture, all three at once; the crawl
//     table keeps updating while a terminal tab is in front
//   - detach any of them: same content, own window, still live. Its close box
//     re-docks it; the X in its own bar ends it
//   - two terminals with different font sizes, opened in either order, both
//     keep their own size -- this is what ui.ThemedAt is for
//   - switch tabs and type: the terminal takes focus back
//   - click a reached device in the crawl table: a session dialog opens
//     prefilled with that device
//   - close the window with a crawl running: it cancels rather than hanging
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"golang.org/x/crypto/ssh"

	"github.com/scottpeterman/pathfinderssh/internal/capture"
	"github.com/scottpeterman/pathfinderssh/internal/capturedial"
	"github.com/scottpeterman/pathfinderssh/internal/capturerun"
	"github.com/scottpeterman/pathfinderssh/internal/crawldial"
	"github.com/scottpeterman/pathfinderssh/internal/crawler"
	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
	helpdoc "github.com/scottpeterman/pathfinderssh/internal/help"
	"github.com/scottpeterman/pathfinderssh/internal/mapweb"
	"github.com/scottpeterman/pathfinderssh/internal/serialx"
	"github.com/scottpeterman/pathfinderssh/internal/sessiondial"
	"github.com/scottpeterman/pathfinderssh/internal/sessions"
	"github.com/scottpeterman/pathfinderssh/internal/storesearch"
	"github.com/scottpeterman/pathfinderssh/internal/term"
	"github.com/scottpeterman/pathfinderssh/internal/topo"
	"github.com/scottpeterman/pathfinderssh/internal/ui"
	"github.com/scottpeterman/pathfinderssh/internal/vault"
	"github.com/scottpeterman/pathfinderssh/internal/vaultcli"
)

func main() {
	var (
		vaultPath    = flag.String("vault", "", "vault file; defaults to the standard path, unlocked from the keyring if it can be")
		sessionsPath = flag.String("sessions", "", "session tree file; defaults to sessions.yaml beside the vault")
		storePath    = flag.String("store", "", "capture store root for the capture applet")
		appTheme     = flag.String("app", "dark", "application chrome: dark|light")
		domain       = flag.String("domain", "", "default domain suffix for crawl and capture")
		verbose      = flag.Bool("v", false, "log applet progress to stderr")
	)
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// Settings come off disk before anything is built: the chrome variant
	// is read by app.New's theme and by every widget after it, so a
	// settings load that happened later would repaint a window that had
	// already been drawn in the other colour.
	//
	// A load failure is not fatal -- LoadSettings hands back working
	// defaults with it -- but it is not silent either, because the next
	// Save overwrites whatever could not be read. It is reported once the
	// window exists to report it in.
	settingsPath := ui.SettingsPath()
	base, settingsErr := ui.LoadSettings(settingsPath)

	// The flag wins over the file, but only when it was actually typed.
	// Its default is "dark", so an unconditional assignment would mean a
	// saved light theme lost every launch to a flag nobody passed.
	if flagWasSet("app") {
		base.AppTheme = ui.AppVariant(*appTheme)
	}
	ui.SetSettings(base)
	base = ui.CurrentSettings()

	// Layer ~/.pathfinderssh/themes over the built-ins and the embedded
	// pack. Nothing called this: theme_registry's init registers the
	// shipped themes and its comment says user themes arrive "at
	// LoadUserThemes() in main", but no main did, so the themes directory
	// has been read by nothing. It has to happen before the first theme
	// lookup, and it is only file reading -- no widget, no app.
	ui.LoadUserThemes()

	// app.New() before ANY widget. Fyne resolves the theme and driver
	// through the current app; building a widget first nil-derefs inside
	// Button.CreateRenderer and the panic names a layout function.
	a := app.New()
	ui.ApplyAppTheme(a, base.AppVariant())
	w := a.NewWindow("PathfinderSSH")
	w.Resize(fyne.NewSize(1280, 820))

	h := &host{
		app:          a,
		win:          w,
		base:         base,
		settingsPath: settingsPath,
		vaultPath:    ui.ExpandHome(*vaultPath),
		verbose:      *verbose,
	}
	if h.vaultPath == "" {
		h.vaultPath = vaultcli.DefaultPath()
	}
	h.shell = ui.NewShell(a, w)

	// Seed the dialogs so the first launch is not an empty form.
	h.lastCrawl.Params = crawlrun.Defaults()
	h.lastCrawl.Verbose = *verbose
	h.lastCapture.Params = capturerun.Defaults()
	h.lastCapture.Params.StorePath = ui.ExpandHome(*storePath)
	h.lastCapture.Verbose = *verbose
	if d := strings.TrimSpace(*domain); d != "" {
		h.lastCrawl.Params.Domains = []string{d}
		h.lastCapture.Params.Domains = []string{d}
	}
	h.node = sessions.Defaults()

	h.shell.AddLauncher("Terminal", theme.ComputerIcon(), func() { h.launchTerminal(h.node) })
	h.shell.AddLauncher("Crawl", theme.SearchIcon(), h.launchCrawl)
	h.shell.AddLauncher("Capture", theme.DownloadIcon(), h.launchCapture)
	h.shell.AddLauncher("Map", theme.GridIcon(), h.launchMap)
	h.shell.AddLauncher("Search", theme.SearchReplaceIcon(), h.launchSearch)

	// Tabs is not a launcher -- it acts on what is already open -- but it sits
	// in the same bar because that is where a person looks for it. The menu
	// form rather than three buttons: closing everything is rare enough that it
	// should cost a deliberate second click, and a bare "Close All" button one
	// pixel from "Terminal" is a mis-click that ends ten sessions.
	h.shell.AddToolbar(h.tabsButton())

	h.buildSessionTree(ui.ExpandHome(*sessionsPath))
	h.buildMenu()

	h.vaultBtn = widget.NewButtonWithIcon("", theme.LoginIcon(), h.showVaultDialog)
	h.vaultBtn.Importance = widget.LowImportance
	h.shell.AddToolbar(h.vaultBtn)

	// Try the keyring and the environment before the window is up, and
	// leave the vault LOCKED if neither has it. The old code called
	// vaultcli.Open here, which falls through to a terminal read -- fine
	// for a CLI, and in a window it blocks on a prompt nobody can see.
	h.unlockQuiet()
	ui.SetHelp(ui.HelpConfig{Version: version})

	w.SetContent(h.shell.Content())
	w.SetMaster()
	w.SetCloseIntercept(func() {
		// Tear the applets down before the window goes. Closing a
		// transport can block, so each instance's OnClose already runs
		// on its own goroutine -- this just makes sure they all start.
		h.shell.CloseAll()
		// Stop answering the browser. A map left open in a tab after
		// the application exits should fail honestly rather than look
		// live until something is clicked.
		if h.maps != nil {
			_ = h.maps.Close()
		}
		w.Close()
	})
	// Report an unreadable settings file now that there is a window to
	// report it in. The application is already running on the defaults;
	// this exists so that the next Save -- which will overwrite the file
	// that could not be read -- is not a surprise.
	if settingsErr != nil {
		log.Printf("[settings] %v", settingsErr)
		dialog.ShowError(settingsErr, w)
	}

	// Immediately before ShowAndRun, like an applet's Start: the watchdog
	// hands work to fyne.Do and needs a running driver.
	h.shell.StartFocusWatch()
	w.ShowAndRun()
}

// host owns everything the shell deliberately does not: the vault, the dialers,
// and the last values each dialog was filled with.
type host struct {
	app  fyne.App
	win  fyne.Window
	base ui.Settings

	shell *ui.Shell

	// vault is the one unlocked vault for this app session. Every applet
	// shares it: the session dialog's credential picker, and both run
	// builders, which are handed the open vault rather than a path so
	// neither can decide to open one on its own.
	vault    *vault.Vault
	vaultBtn *widget.Button

	creds  []string
	lookup sessiondial.Lookup

	// defaultCred is the vault's default credential name, or "" when there
	// is none. Held so a dialog can SAY which credential a blank field
	// resolves to without being handed the credential itself.
	defaultCred string
	vaultPath   string
	verbose     bool

	// settingsPath is the file the settings dialog reads and writes. It is
	// held rather than recomputed so a run can only ever have one answer
	// for where its settings live.
	settingsPath string

	// tree is the saved inventory, docked down the left. The host owns the
	// FILE; the widget owns the display and hands back a tree to save.
	tree         *ui.SessionTree
	sessionsPath string

	node        sessions.Node
	lastCrawl   ui.CrawlLaunch
	lastCapture ui.CaptureLaunch
	lastSearch  ui.SearchLaunch

	// maps is the loopback viewer, started on the first map opened and
	// kept for the life of the app: one port, one token, and a browser tab
	// that keeps working when the next map is loaded into it.
	maps   *mapweb.Server
	mapDir string
}

func (h *host) logf() func(string, ...any) { return h.logfIf(false) }

// logfIf is logf with a per-run override.
//
// Both launch dialogs carry a "Log progress to stderr" checkbox and neither
// run path was reading it: every logf came from h.verbose, which is the -v
// flag and nothing else. So the checkbox did nothing, and the one thing it
// would have shown — the credential-resolution lines naming which vault entry
// was offered to which device — was unreachable from the GUI at all.
func (h *host) logfIf(runVerbose bool) func(string, ...any) {
	if !h.verbose && !runVerbose {
		return func(string, ...any) {}
	}
	return func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// --- terminal --------------------------------------------------------------

// launchTerminal opens the session dialog and connects what comes out of it.
func (h *host) launchTerminal(start sessions.Node) {
	h.launchTerminalTitled("New session", start)
}

// launchTerminalTitled is the same dialog under a caller-chosen heading. A
// node picked out of the inventory is not a new session, and a dialog that
// says it is reads like the click did something other than it did.
func (h *host) launchTerminalTitled(title string, start sessions.Node) {
	var d dialog.Dialog

	form := ui.NewSessionForm(ui.SessionFormOptions{
		Node:              start,
		Credentials:       h.creds,
		DefaultCredential: h.defaultCred,
		VaultLocked:       h.lookup == nil,
		ListSerialPorts:   listPorts,
		ShowConnect:       true,
		OnConnect: func(n sessions.Node) {
			h.node = n
			d.Hide()
			h.connect(n)
		},
	})

	d = dialog.NewCustom(title, "Cancel", form.Content(), h.win)
	d.Resize(fyne.NewSize(760, 660))
	d.Show()
}

// --- session tree ----------------------------------------------------------

// buildSessionTree loads the inventory and docks it beside the tabs.
//
// A file that will not parse is reported and then ignored: starting with an
// empty tree beats refusing to start, and the file is left untouched so it can
// be fixed in an editor rather than overwritten by whatever this session does
// next.
func (h *host) buildSessionTree(path string) {
	h.sessionsPath = path
	if h.sessionsPath == "" {
		// Beside the vault, deliberately, so the legacy-directory fallback
		// vaultcli already does is inherited rather than reimplemented.
		h.sessionsPath = filepath.Join(filepath.Dir(h.vaultPath), "sessions.yaml")
	}

	tr, err := sessions.LoadFile(h.sessionsPath)
	if err != nil {
		log.Printf("[sessions] %s: %v", h.sessionsPath, err)
	}

	h.tree = ui.NewSessionTree(ui.SessionTreeOptions{
		Window: h.win,
		OnActivate: func(folder string, n sessions.Node) {
			h.launchTerminalTitled(n.Label(), n)
		},
		OnNew: func(folder string, apply func(sessions.Node)) {
			h.editSession("New session in "+folder, sessions.Defaults(), apply)
		},
		OnEdit: func(folder string, n sessions.Node, apply func(sessions.Node)) {
			h.editSession("Edit "+n.Label(), n, apply)
		},
		OnChanged: h.saveTree,
	})
	h.tree.SetTree(tr)
	h.shell.SetSide(h.tree.Content(), 0.25)

	if err != nil {
		// After the window has content, or the error has nowhere to appear.
		fyne.Do(func() {
			dialog.ShowError(fmt.Errorf("could not read %s: %w", h.sessionsPath, err), h.win)
		})
	}
}

// editSession is the inventory's session dialog: Save writes it back to the
// tree, Connect saves AND dials. Saving on connect is deliberate — editing a
// node in order to reach a box, connecting, and finding the edit gone is a
// small betrayal that happens every time.
func (h *host) editSession(title string, start sessions.Node, apply func(sessions.Node)) {
	var d dialog.Dialog

	form := ui.NewSessionForm(ui.SessionFormOptions{
		Node:              start,
		Credentials:       h.creds,
		DefaultCredential: h.defaultCred,
		VaultLocked:       h.lookup == nil,
		ListSerialPorts:   listPorts,
		ShowSave:          true,
		ShowConnect:       true,
		OnSave: func(n sessions.Node) {
			d.Hide()
			apply(n)
		},
		OnConnect: func(n sessions.Node) {
			d.Hide()
			apply(n)
			h.connect(n)
		},
	})

	d = dialog.NewCustom(title, "Cancel", form.Content(), h.win)
	d.Resize(fyne.NewSize(760, 660))
	d.Show()
}

// folderFor finds the folder holding this node, reporting false when the answer
// is not exactly one.
//
// Both failure directions matter and neither is an error: a session dialled ad
// hoc is in no folder, and a device name that appears in two site folders has
// no single right answer. Writing to a guess would edit the wrong device's
// session file, which is worse than not offering to write at all.
func (h *host) folderFor(n sessions.Node) (string, bool) {
	label := n.Normalize().Label()
	if label == "" {
		return "", false
	}
	target := n.Target()
	found := ""
	for _, f := range h.tree.Tree().Folders {
		for _, s := range f.Sessions {
			if s.Label() != label || s.Target() != target {
				continue
			}
			if found != "" {
				return "", false
			}
			found = f.Name
		}
	}
	return found, found != ""
}

// rememberPastePacing persists a pacing pair chosen in the paste confirmation.
//
// Zero cannot be stored as-is for either field: on a node zero means "inherit
// the application setting", and the application default for the line delay is
// 25ms. So an operator who picked "No delay" and ticked remember would get 25ms
// back on the next connect — the setting silently not taking, which is the
// worst outcome of the three. Negative is the established spelling for an
// explicit off (see Node.PasteLineDelayMs), and ConsoleBaud now reads the same
// way.
func (h *host) rememberPastePacing(folder string, n sessions.Node, delayMs, baud int) {
	if delayMs <= 0 {
		delayMs = -1
	}
	if baud <= 0 {
		baud = -1
	}

	tr := h.tree.Tree()
	i := tr.FolderIndex(folder)
	if i < 0 {
		dialog.ShowError(fmt.Errorf("no folder called %q", folder), h.win)
		return
	}
	label := n.Normalize().Label()
	j := tr.Folders[i].SessionIndex(label)
	if j < 0 {
		dialog.ShowError(fmt.Errorf("no session called %q in %q", label, folder), h.win)
		return
	}

	// Read the node back out of the tree rather than editing the copy this
	// tab was dialled from. That copy is a snapshot taken at connect time,
	// and writing it back would revert anything edited in the session form
	// since -- an unrelated change lost as a side effect of a paste.
	upd := tr.Folders[i].Sessions[j]
	upd.PasteLineDelayMs = delayMs
	upd.ConsoleBaud = baud
	if err := tr.Replace(folder, label, upd); err != nil {
		dialog.ShowError(err, h.win)
		return
	}
	h.tree.SetTree(tr)
	h.saveTree(tr)
	log.Printf("[paste] remembered pacing for %s: delay=%d baud=%d", label, delayMs, baud)
}

// saveTree writes the inventory. A failed save is raised, never swallowed: the
// widget has already redrawn as though it worked, so silence here means the
// person believes an edit is saved when it is not.
func (h *host) saveTree(tr sessions.Tree) {
	if err := sessions.SaveFile(h.sessionsPath, tr); err != nil {
		dialog.ShowError(fmt.Errorf("could not save %s: %w", h.sessionsPath, err), h.win)
	}
}

// --- the File menu ---------------------------------------------------------

// buildMenu puts import and export on a menu bar rather than on the tree panel.
//
// They belong here for the same reason the tree widget has no save button: the
// host owns the FILE and the widget owns the display. They are also the wrong
// shape for that panel — a quarter-width column of icons is for the things done
// constantly, and importing an estate is done once and then not again for
// months.
// tabsButton is the toolbar entry for acting on open tabs.
//
// The items are greyed rather than hidden when they do not apply: a control
// that disappears when there is nothing to close is a control nobody learns
// is there.
func (h *host) tabsButton() *widget.Button {
	var btn *widget.Button
	btn = widget.NewButtonWithIcon("Tabs", theme.ListIcon(), func() {
		open := h.shell.TabCount()
		current := h.shell.Current()

		closeTab := fyne.NewMenuItem("Close Tab", h.shell.CloseCurrent)
		closeTab.Disabled = current == nil

		closeOthers := fyne.NewMenuItem("Close Other Tabs", func() {
			h.confirmClose("Close other tabs?", open-1, func() {
				h.shell.CloseOthers(current)
			})
		})
		closeOthers.Disabled = current == nil || open <= 1

		closeAll := fyne.NewMenuItem("Close All Tabs", func() {
			h.confirmClose("Close all tabs?", open, h.shell.CloseAll)
		})
		closeAll.Disabled = open == 0

		menu := fyne.NewMenu("", closeTab, fyne.NewMenuItemSeparator(), closeOthers, closeAll)
		widget.ShowPopUpMenuAtRelativePosition(
			menu, h.win.Canvas(), fyne.NewPos(0, btn.Size().Height), btn)
	})
	btn.Importance = widget.LowImportance
	return btn
}

// confirmClose asks before ending more than one session at once.
//
// One tab is the person closing the thing in front of them; several is a
// command whose effect they cannot see -- a crawl mid-run and a terminal
// sitting at a config prompt look identical from a menu. Closing exactly one
// skips the question, because a confirmation on every close is a confirmation
// nobody reads.
func (h *host) confirmClose(title string, n int, do func()) {
	if n <= 0 {
		return
	}
	if n == 1 {
		do()
		return
	}
	msg := fmt.Sprintf("Close %d open sessions? Anything still running will be stopped.", n)
	dialog.ShowConfirm(title, msg, func(ok bool) {
		if ok {
			do()
		}
	}, h.win)
}

func (h *host) buildMenu() {
	file := fyne.NewMenu("File",
		fyne.NewMenuItem("Settings…", h.showSettings),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Import session YAML…", h.importSessions),
		fyne.NewMenuItem("Import topology map…", h.importMap),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Export session YAML…", h.exportSessions),
	)

	// A menu as well as the toolbar button. The toolbar answers "is the
	// vault open"; managing what is IN it is a different question, and
	// hanging it off the same button would mean either a dialog that asks
	// which of the two you meant or a lock action one misclick away from a
	// credential list.
	vaultMenu := fyne.NewMenu("Vault",
		fyne.NewMenuItem("Manage credentials…", h.manageVault),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Unlock / lock…", h.showVaultDialog),
	)

	// No Quit item: Fyne appends one to the first menu itself, and its
	// action goes through the window's close intercept — so the applet
	// teardown in SetCloseIntercept still runs. Adding one here would
	// either duplicate it or replace it with a quit that skipped that.
	helpMenu := fyne.NewMenu("Help",
		fyne.NewMenuItem("Quickstart", func() { ui.ShowHelp(h.win, helpdoc.TopicQuickstart) }),
		fyne.NewMenuItem("Contents", func() { ui.ShowHelp(h.win, "") }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("About "+ui.DefaultAppName+"…", h.showAbout),
	)
	h.win.SetMainMenu(fyne.NewMainMenu(file, vaultMenu, helpMenu))
}

// pickFile opens a read picker filtered to one set of extensions and hands the
// whole file to use. Reading here rather than in each caller keeps the three
// menu actions about what the bytes MEAN.
//
// A nil reader is a cancel, not a failure, and says nothing.
func (h *host) pickFile(exts []string, dir string, use func(path string, data []byte)) {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, h.win)
			return
		}
		if r == nil {
			return
		}
		defer r.Close()

		data, err := io.ReadAll(r)
		if err != nil {
			dialog.ShowError(fmt.Errorf("read %s: %w", r.URI().Name(), err), h.win)
			return
		}
		use(r.URI().Path(), data)
	}, h.win)

	d.SetFilter(storage.NewExtensionFileFilter(exts))
	if l := listerFor(dir); l != nil {
		d.SetLocation(l)
	}
	d.Resize(fyne.NewSize(820, 600))
	d.Show()
}

// listerFor turns a directory into something the picker can start in, or nil.
// A directory that has gone away is not worth a message — the picker opens
// wherever it would have opened anyway.
func listerFor(dir string) fyne.ListableURI {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	l, err := storage.ListerForURI(storage.NewFileURI(dir))
	if err != nil {
		return nil
	}
	return l
}

// importSessions merges another session file into this one.
//
// The file's own folders are kept. Which reader runs is decided by the shape of
// the document rather than by its name, so this also accepts the older
// terminal's file without the person having to say which kind it is — and says
// so when the file picked is a map.
func (h *host) importSessions() {
	h.pickFile([]string{".yaml", ".yml"}, filepath.Dir(h.sessionsPath), func(path string, data []byte) {
		folders, format, err := sessions.FoldersFrom(data)
		if err != nil {
			dialog.ShowError(fmt.Errorf("%s: %w", filepath.Base(path), err), h.win)
			return
		}
		tr := h.tree.Tree()
		h.applyImport(tr, format, tr.ImportFolders(folders))
	})
}

// importMap turns a crawl's map.json into sessions.
//
// One flat folder per import, because a map has no structure worth keeping —
// the person's folders are theirs to make, and a crawl has no opinion about
// which of 600 devices belong together.
func (h *host) importMap() {
	dir := h.mapDir
	if dir == "" && h.lastCrawl.MapPath != "" {
		dir = filepath.Dir(h.lastCrawl.MapPath)
	}

	h.pickFile([]string{".json"}, dir, func(path string, data []byte) {
		h.mapDir = filepath.Dir(path)
		if f := sessions.Sniff(data); f != sessions.FormatMap {
			dialog.ShowError(fmt.Errorf("%s is a %s, not a topology map", filepath.Base(path), f), h.win)
			return
		}
		h.askMapImport(mapFolderName(path), data)
	})
}

// mapFolderName is the folder an imported map lands in unless the person says
// otherwise. A crawl writes its map into a directory named after what was
// crawled and calls the file map.json, so for that layout the DIRECTORY is the
// name worth offering; anything else uses the filename.
func mapFolderName(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.EqualFold(base, "map") {
		if dir := filepath.Base(filepath.Dir(path)); dir != "" && dir != "." && dir != string(filepath.Separator) {
			return dir
		}
	}
	return base
}

// askMapImport collects the folder name and the one decision a map import has.
//
// Leaves are the devices a neighbour named and nothing ever dialled. They are
// the bulk of a real map — hundreds of servers behind an exclude — and nothing
// is known about them but a name, so they are off unless the leaf IS the target.
func (h *host) askMapImport(defaultFolder string, data []byte) {
	name := widget.NewEntry()
	name.SetText(defaultFolder)
	name.SetPlaceHolder("Site, role, customer…")

	leaves := widget.NewCheck("", nil)
	leafItem := widget.NewFormItem("Include leaves", leaves)
	leafItem.HintText = "devices a neighbour reported but the crawl never dialled"

	dialog.ShowForm("Import topology map", "Import", "Cancel",
		[]*widget.FormItem{
			widget.NewFormItem("Folder", name),
			leafItem,
		},
		func(ok bool) {
			if !ok {
				return
			}
			nodes, err := sessions.NodesFromMap(data, leaves.Checked)
			if err != nil {
				dialog.ShowError(err, h.win)
				return
			}
			folder := strings.TrimSpace(name.Text)
			if folder == "" {
				folder = "Imported"
			}

			// The same merge the session-file import uses, so both report
			// themselves identically and a device already in the tree is
			// skipped wherever it was filed.
			tr := h.tree.Tree()
			sum := tr.ImportFolders([]sessions.Folder{{Name: folder, Sessions: nodes}})
			h.applyImport(tr, sessions.FormatMap, sum)
		}, h.win)
}

// applyImport puts the merged tree back, saves it, and says what happened.
//
// SetTree deliberately does not fire OnChanged — that callback is the widget
// telling the host something changed, and this is the host telling the widget
// what is true. So the save is explicit, and it happens before the summary: a
// dialog saying 13 sessions were added, over a file that was never written, is
// the failure worth ruling out.
func (h *host) applyImport(tr sessions.Tree, format sessions.Format, sum sessions.ImportSummary) {
	h.tree.SetTree(tr)
	h.saveTree(tr)
	dialog.ShowInformation("Imported "+format.String(), sum.Describe(), h.win)
}

// exportSessions writes the whole tree to a file of the person's choosing.
//
// What leaves is what is on disk already: folders, sessions, and a credential
// REFERENCE by name. No password and no passphrase, because the model marks
// both yaml:"-" and a test fails if either reaches the bytes — so a file handed
// to somebody else carries a map of the estate and nothing that unlocks it.
func (h *host) exportSessions() {
	tr := h.tree.Tree()

	// Marshal BEFORE the picker. The save dialog creates the file it
	// returns, and a marshal that failed after that would leave an empty
	// file where the person believes their inventory is.
	data, err := sessions.MarshalTree(tr)
	if err != nil {
		dialog.ShowError(fmt.Errorf("render the session file: %w", err), h.win)
		return
	}
	count := len(tr.Nodes())

	d := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, h.win)
			return
		}
		if wc == nil {
			return
		}
		defer wc.Close()

		if _, err := wc.Write(data); err != nil {
			dialog.ShowError(fmt.Errorf("write %s: %w", wc.URI().Name(), err), h.win)
			return
		}
		dialog.ShowInformation("Exported",
			fmt.Sprintf("Wrote %d session(s) to %s.", count, wc.URI().Name()), h.win)
	}, h.win)

	d.SetFileName("sessions.yaml")
	d.SetFilter(storage.NewExtensionFileFilter([]string{".yaml", ".yml"}))
	if l := listerFor(filepath.Dir(h.sessionsPath)); l != nil {
		d.SetLocation(l)
	}
	d.Resize(fyne.NewSize(820, 600))
	d.Show()
}

func (h *host) connect(n sessions.Node) {
	progress := dialog.NewCustomWithoutButtons("Connecting",
		widget.NewLabel("Connecting to "+n.Target()+" …"), h.win)
	progress.Show()

	opts := sessiondial.Options{
		Credentials:   h.lookup,
		HostKeyPrompt: h.promptHostKey,
		OnNewHostKey: func(host, keyType, fingerprint string) {
			log.Printf("[hostkey] trusted on first contact: %s %s %s", host, keyType, fingerprint)
		},
		AuthPrompt: h.promptSecret,
		Log:        log.Printf,
	}

	// Dial off the UI goroutine. A device slow to answer, or a host-key
	// prompt waiting on a click, must not freeze the window -- and the
	// prompt cannot be answered by a frozen one.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		tp, err := sessiondial.Connect(ctx, n, opts)
		fyne.Do(func() {
			progress.Hide()
			if err != nil {
				log.Printf("[dial] %v", err)
				dialog.ShowError(err, h.win)
				return
			}
			h.mountTerminal(n, tp)
		})
	}()
}

// mountTerminal builds the session and hands it to the shell. UI goroutine.
func (h *host) mountTerminal(n sessions.Node, tp term.Transport) {
	// The settings dance, and the reason it is a dance: Settings are
	// process-wide, and the terminal widget reads FontSize and
	// ScrollbackLines at construction. So install this session's values,
	// build, wrap at an EXPLICIT size, and put the base back immediately --
	// the override object holds the size, so the next tab opening cannot
	// change this one's.
	cfg := ui.SettingsFor(h.base, n)
	ui.SetSettings(cfg)

	sess := ui.NewSession()
	// Before Attach: anti-idle is read when the transport is attached, so
	// setting it afterwards silently does nothing until a reconnect.
	ui.ApplySession(sess, n)
	// Pin the palette rather than letting it fall through to the global,
	// which the next session mounted would move.
	sess.SetTerminalTheme(cfg.TerminalThemeName())
	content := ui.ThemedAt(sess, cfg)

	ui.SetSettings(h.base)

	if err := sess.Attach(tp); err != nil {
		log.Printf("[attach] %v", err)
		tp.Close()
		dialog.ShowError(err, h.win)
		return
	}

	inst := h.shell.Open(ui.Mount{
		Kind:   ui.KindTerminal,
		Title:  n.Label(),
		Applet: &termApplet{content: content},
		Focus:  sess,
		// The terminal resolves its own canvas for focus-on-click and for
		// its context menu, and the driver's cache cannot tell it that it
		// has moved. Without this a detached session goes deaf on the
		// first click and its right-click menu opens on the main window.
		OnCanvasChange: sess.SetHostCanvas,
		// The window is not its final size when it appears, and the
		// terminal's resize is debounced — without this a detached
		// session can tell the far end the minimum window size and a
		// full-screen application redraws into a corner.
		OnPlaced: sess.ResyncSize,
		OnClose: func() {
			if err := sess.Close(); err != nil {
				log.Printf("[close] %v", err)
			}
		},
	})
	inst.SetStatus(n.Target())

	// The terminal's right-click menu carries the same two bulk actions, for
	// the case where the pointer is already in the terminal. It is handed
	// functions rather than the shell: it must not learn what a tab is.
	sess.SetTabHooks(
		func() { h.confirmClose("Close all tabs?", h.shell.TabCount(), h.shell.CloseAll) },
		func() {
			h.confirmClose("Close other tabs?", h.shell.TabCount()-1, func() { h.shell.CloseOthers(inst) })
		},
		h.shell.TabCount,
	)

	// The paste confirmation offers to remember a pacing override, but only
	// when there is somewhere to remember it. A session dialled from a map
	// click or typed into the form is not in the inventory, so the hook stays
	// nil and the dialog hides the checkbox rather than promising a save that
	// has no file to land in.
	if folder, ok := h.folderFor(n); ok {
		sess.SetPasteRememberFunc(func(delayMs, baud int) {
			h.rememberPastePacing(folder, n, delayMs, baud)
		})
	}

	sess.SetStateChangeHandler(func(st ui.ConnectionState) {
		log.Printf("[state] %s %s", n.Label(), st)
		// The handler can fire from the read loop, so hop threads before
		// touching a widget.
		fyne.Do(func() { inst.SetStatus(n.Target() + " — " + st.String()) })
	})
	sess.SetErrorHandler(func(err error) { log.Printf("[error] %s: %v", n.Label(), err) })

	h.win.Canvas().Focus(sess)
}

// termApplet is the whole adapter. A terminal has no redraw loop to gate -- it
// repaints from its read loop -- so Start and Stop are genuinely nothing, and
// the teardown that matters is the Mount's OnClose.
type termApplet struct {
	content fyne.CanvasObject
}

func (t *termApplet) Content() fyne.CanvasObject { return t.content }
func (t *termApplet) Start()                     {}
func (t *termApplet) Stop()                      {}

// --- crawl -----------------------------------------------------------------

func (h *host) launchCrawl() {
	h.lastCrawl.Params.VaultPath = h.runVaultPath()
	ui.ShowCrawlDialog(h.win, h.lastCrawl, func(l ui.CrawlLaunch) {
		l.Params.VaultPath = h.runVaultPath()
		h.lastCrawl = l
		h.startCrawl(l)
	})
}

func (h *host) startCrawl(l ui.CrawlLaunch) {
	run := crawlrun.New()
	view := ui.NewCrawlView(run)

	// The loop the shell exists to close: a device in a crawl result is a
	// device you can open a session to without retyping it.
	view.OnConnect = func(d crawlrun.DeviceRow) {
		n := sessions.Defaults()
		n.Name = d.Display()
		n.Host = d.Name
		n.Transport = sessions.TransportSSH
		h.launchTerminal(n)
	}

	ctx, cancel := context.WithCancel(context.Background())

	var stop *widget.Button
	stop = widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		cancel()
		stop.SetText("Stopping…")
		stop.Disable()
	})

	title := "crawl"
	if len(l.Params.Seeds) > 0 {
		title = "crawl " + l.Params.Seeds[0]
	}

	inst := h.shell.Open(ui.Mount{
		Kind:    ui.KindCrawl,
		Title:   title,
		Applet:  view,
		Actions: []fyne.CanvasObject{stop},
		// Cancel on close as well as on Stop. Closing the tab of a
		// running crawl and leaving it dialing devices in the background
		// is the kind of thing that is only noticed by the lockout
		// counter on somebody's TACACS server.
		OnClose: cancel,
	})
	inst.SetStatus(fmt.Sprintf("%d seed(s) · depth %d", len(l.Params.Seeds), l.Params.Depth))

	if l.LastRun != "" {
		if prev, err := crawlrun.LoadSnapshot(l.LastRun); err == nil {
			view.Compare(prev)
		} else {
			inst.SetStatus("comparison unavailable: " + err.Error())
		}
	}

	logf := h.logfIf(l.Verbose)

	// outputs accumulates what this run actually wrote, and is read by the
	// deferred summary. A crawl that produced no file has to SAY so: the
	// map and the snapshot are the durable half of the work, the counters
	// look identical either way, and a blank path field reads exactly like
	// a write that failed.
	var outputs []string
	go func() {
		defer func() {
			run.Finish()
			c := run.Counts()
			summary := fmt.Sprintf(
				"%d reached · %d failed · %d not dialed · %d new host keys · %.2f tries/device",
				c.Reached, c.Failed, c.NotDialed, c.NewHostKeys, c.AttemptsPerReached())
			if len(outputs) > 0 {
				summary += "  ·  " + strings.Join(outputs, ", ")
			} else {
				summary += "  ·  nothing written"
			}
			fyne.Do(func() {
				stop.SetText("Done")
				stop.Disable()
				inst.SetStatus(summary)
			})
		}()

		built, err := crawldial.Build(l.Params, crawldial.Options{
			// The OPEN vault, not a path. Build must never try to
			// unlock one from here: it runs on this goroutine and
			// has nowhere to ask for a master password.
			Vault: h.vault,
			Static: crawldial.StaticCreds{
				Username: l.Auth.Username, Password: l.Auth.Password, KeyPath: l.Auth.KeyPath,
			},
			Log:     crawler.Logf(logf),
			CredLog: logf,
			Emit:    run.Emit(),
		})
		if err != nil {
			// A dialog, not just the status line. The commonest
			// cause is a locked vault, and that is a thing to fix
			// and retry rather than a result to read.
			h.reportRunError(inst, err)
			return
		}
		defer built.Close()

		devices := built.Crawler.CrawlContext(ctx, l.Params.Seeds)

		// The same two writes cmd/crawl makes, for the same reasons: the
		// fold is the only place SysName reaches the binding store, and
		// the snapshot is what makes the NEXT run's comparison real.
		crawldial.Fold(built.Bindings, devices, l.Params.Domains, crawler.Logf(logf))

		// Both writes report, and a failure raises a dialog rather than
		// a log line. logf is a no-op without -v, so the previous
		// version of this could fail to write the map and end with a
		// summary that looked like a clean run.
		if l.MapPath != "" {
			if err := writeMap(devices, l.Params, l.MapPath); err != nil {
				logf("pathfinder: %v", err)
				h.reportRunError(inst, err)
			} else {
				outputs = append(outputs, "map → "+l.MapPath)
			}
		}
		if l.SaveRun != "" {
			if err := run.Snapshot(l.Params.Seeds, l.Params.Domains).Save(l.SaveRun); err != nil {
				logf("pathfinder: %v", err)
				h.reportRunError(inst, err)
			} else {
				outputs = append(outputs, "run → "+l.SaveRun)
			}
		}
	}()
}

// writeMap renders the topology and writes it, reporting every failure.
//
// The marshal error was previously discarded by an `if err == nil` with no
// else, so a map that could not be encoded wrote nothing and said nothing.
func writeMap(devices []*topo.Device, p crawlrun.Params, path string) error {
	m := topo.Generate(devices, crawldial.MapOptions(p))
	data, err := topo.MarshalMap(m)
	if err != nil {
		return fmt.Errorf("render map: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// The path names a file the operator chose; making its parent
		// is part of honouring that, and refusing to because one
		// directory is missing is the kind of friction that gets a
		// crawl re-run for nothing.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// reportRunError puts a failure in front of the user instead of leaving it in
// the status line.
//
// Called from a run goroutine, so it hops threads. A dialog because a status
// line is easy to miss and, at the end of a run, about to be overwritten by
// the final counts — and because these are all things to fix and retry rather
// than results to read.
// --- map -------------------------------------------------------------------

// launchMap opens the picker. The folder it starts in is the one last used,
// or the folder the last crawl wrote its map to — after a crawl that is
// exactly where the person wants to be looking.
func (h *host) launchMap() {
	dir := h.mapDir
	if dir == "" && h.lastCrawl.MapPath != "" {
		dir = filepath.Dir(h.lastCrawl.MapPath)
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dir = home
		}
	}

	ui.ShowMapDialog(h.win, dir, func(l ui.MapLaunch) {
		h.mapDir = l.Dir
		h.openMap(l.File)
	})
}

// openMap loads a map into the viewer and opens it in the browser.
//
// Rendering lives outside this process on purpose: a browser already has a
// graph engine, a zoom, a scroll and a print dialog, and none of that has to
// be built or maintained here. What stays in the application is the part only
// the application can do — knowing which device a node is, and opening a
// session to it.
func (h *host) openMap(f mapweb.MapFile) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		dialog.ShowError(err, h.win)
		return
	}

	if h.maps == nil {
		srv, err := mapweb.Serve(mapweb.Options{
			OnConnect: h.mapConnect,
			Log:       h.logf(),
		})
		if err != nil {
			dialog.ShowError(fmt.Errorf("start the map viewer: %w", err), h.win)
			return
		}
		h.maps = srv
	}

	if err := h.maps.SetMap(f.Name, data); err != nil {
		dialog.ShowError(fmt.Errorf("%s: %w", f.Name, err), h.win)
		return
	}

	u, err := url.Parse(h.maps.URL())
	if err != nil {
		dialog.ShowError(err, h.win)
		return
	}
	if err := h.app.OpenURL(u); err != nil {
		// Not fatal: the server is up and the URL is valid, so give it
		// to the person rather than dropping the map on the floor.
		dialog.ShowInformation("Open this in a browser",
			h.maps.URL(), h.win)
	}
}

// mapConnect is the click-to-session loop, arriving from the browser.
//
// Two things about it are deliberate. It runs on the map server's HTTP
// goroutine, so everything it touches goes through fyne.Do. And it opens the
// session DIALOG rather than dialing: a click that arrives over HTTP should
// end in a form the person confirms, not in an SSH connection nobody asked
// for. That is also what makes the loopback surface safe to leave running.
func (h *host) mapConnect(n mapweb.NodeRef) {
	node := sessions.Defaults()
	node.Name = n.Name
	node.Transport = sessions.TransportSSH

	// Prefer the address. A neighbour-reported name is often not resolvable
	// from here, and his lab has no DNS at all — the IP fallback is the
	// path that actually works.
	if n.IP != "" {
		node.Host = n.IP
	} else {
		node.Host = n.Name
	}

	fyne.Do(func() {
		h.launchTerminal(node)
		// The browser has focus when a node is clicked, so without this
		// the session dialog opens behind it and has to be gone looking
		// for. No-op under Wayland by Fyne's design, where the
		// compositor decides.
		h.win.RequestFocus()
	})
}

func (h *host) reportRunError(inst *ui.Instance, err error) {
	fyne.Do(func() {
		dialog.ShowError(err, h.win)
		inst.SetStatus("⚠  " + err.Error())
	})
}

// --- capture ---------------------------------------------------------------

// launchSearch collects a query and runs it against a capture store.
//
// The store field is seeded from the last capture launch in this process, then
// from the -store flag. A search dialog that opens with a blank store path
// makes the operator type a path to a folder the app already knows about,
// which is how a feature ends up unused.
func (h *host) launchSearch() {
	if h.lastSearch.StorePath == "" {
		h.lastSearch.StorePath = h.lastCapture.Params.StorePath
	}
	ui.ShowSearchDialog(h.win, h.lastSearch, capturedial.KnownTypes(), func(l ui.SearchLaunch) {
		h.lastSearch = l
		h.startSearch(l)
	})
}

func (h *host) startSearch(l ui.SearchLaunch) {
	// A dialog rather than a status line, and before any tab is opened:
	// there is nothing yet to put a status on, and an empty search tab
	// with a quiet failure in it reads as a store with nothing in it.
	store, err := capture.OpenFileStore(l.StorePath)
	if err != nil {
		dialog.ShowError(err, h.win)
		return
	}
	matcher, err := storesearch.NewLiteral(l.Query, l.CaseSensitive)
	if err != nil {
		dialog.ShowError(err, h.win)
		return
	}

	view := ui.NewSearchView(store)
	view.SetMatcher(matcher)
	// The store keys on the canonical device name, which is the same
	// identity the binding store and the session tree use — so a hit
	// becomes a session through exactly the path a map click already
	// takes. There is no address here: capture files under the name, and
	// resolving it is the dialer's job, not the view's.
	view.OnConnect = func(device string) { h.mapConnect(mapweb.NodeRef{Name: device}) }

	ctx, cancel := context.WithCancel(context.Background())

	var stop *widget.Button
	stop = widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		cancel()
		stop.SetText("Stopping…")
		stop.Disable()
	})

	inst := h.shell.Open(ui.Mount{
		Kind: ui.KindSearch,
		// The query IS the title. Several searches coexist the way two
		// crawls already do, and a tab strip of tabs all called
		// "search" is a tab strip nobody can navigate.
		Title:   "search " + l.Query,
		Applet:  view,
		Actions: []fyne.CanvasObject{stop},
		OnClose: cancel,
	})
	inst.SetStatus(filepath.Base(l.StorePath))

	go func() {
		res, err := storesearch.Search(ctx, store, matcher, storesearch.Options{
			Types:      l.Types,
			Limit:      l.Limit,
			OnProgress: view.SetProgress,
		})
		fyne.Do(func() {
			stop.SetText("Done")
			stop.Disable()
		})
		// A cancelled search still carries the hits it found before it
		// was stopped, so the result is installed either way and the
		// cancellation is reported as status rather than as failure.
		view.SetResult(res)
		if err != nil && ctx.Err() == nil {
			view.SetError(err)
			return
		}
		fyne.Do(func() { inst.SetStatus(res.Summary()) })
	}()
}

func (h *host) launchCapture() {
	h.lastCapture.Params.VaultPath = h.runVaultPath()
	// Offer the inventory this window is already showing. Without it the
	// session field opens blank and the tree is only reachable by typing
	// a path to a file that is on screen — and a device source nobody
	// finds is a device source nobody uses. It stays only an offer: no
	// pattern means no sessions are selected.
	if h.lastCapture.Params.SessionFile == "" {
		h.lastCapture.Params.SessionFile = h.sessionsPath
	}
	ui.ShowCaptureDialog(h.win, h.lastCapture, capturedial.KnownTypes(), func(l ui.CaptureLaunch) {
		l.Params.VaultPath = h.runVaultPath()
		h.lastCapture = l
		h.startCapture(l)
	})
}

func (h *host) startCapture(l ui.CaptureLaunch) {
	run := capturerun.New()

	// A browser without a run is first class: opening the store to read
	// last night's config is the expected use once captures are scheduled.
	var browser capture.Browser
	if l.Params.StorePath != "" {
		if store, err := capture.OpenFileStore(l.Params.StorePath); err == nil {
			browser = store
		} else {
			log.Printf("[store] %s: %v", l.Params.StorePath, err)
		}
	}

	view := ui.NewCaptureView(run, browser)

	ctx, cancel := context.WithCancel(context.Background())

	var stop *widget.Button
	stop = widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		cancel()
		stop.SetText("Stopping…")
		stop.Disable()
	})

	title := "capture"
	if l.Params.StorePath != "" {
		title = "capture " + filepath.Base(l.Params.StorePath)
	}

	inst := h.shell.Open(ui.Mount{
		Kind:    ui.KindCapture,
		Title:   title,
		Applet:  view,
		Actions: []fyne.CanvasObject{stop},
		OnClose: cancel,
	})

	if !l.Params.HasDeviceSource() {
		msg := "no devices — browsing the store"
		if browser == nil {
			msg = "no devices and no store"
		}
		inst.SetStatus(msg)
		stop.Disable()
		return
	}

	logf := h.logfIf(l.Verbose)
	go func() {
		defer func() {
			run.Finish()
			c := run.Counts()
			fyne.Do(func() {
				stop.SetText("Done")
				stop.Disable()
				inst.SetStatus(fmt.Sprintf(
					"%d stored · %d unchanged · %d not applicable · %d failed · %d new host key(s)",
					c.Stored, c.Unchanged, c.NotApplicable, c.Failed, c.NewHostKeys))
			})
		}()

		built, err := capturedial.Build(l.Params, capturedial.Options{
			Vault: h.vault,
			Static: capturedial.StaticCreds{
				Username: l.Auth.Username, Password: l.Auth.Password, KeyPath: l.Auth.KeyPath,
			},
			Log:     logf,
			CredLog: logf,
			Emit:    run.Emit(),
		})
		if err != nil {
			h.reportRunError(inst, err)
			return
		}
		defer built.Close()

		// What is about to happen, before it happens. A capture is the
		// case where being told afterwards is too late, and the CGNAT
		// notes in particular are decisions rather than trivia.
		plan := fmt.Sprintf("%d device(s) x %d type(s) -> %s",
			len(built.Devices), len(built.Specs), l.Params.StorePath)
		if len(built.Skipped) > 0 {
			// Sessions a pattern matched that capture cannot visit.
			// Without this the shell shows a smaller device count
			// than the pattern matched and says nothing about the
			// difference, which reads as the pattern being wrong.
			plan += fmt.Sprintf("  ·  %d matched session(s) skipped: %s",
				len(built.Skipped), strings.Join(capturedial.SkippedLines(built.Skipped), "; "))
		}
		if len(built.Notes) > 0 {
			var notes []string
			for id, n := range built.Notes {
				notes = append(notes, id+": "+n)
			}
			plan += "  ·  " + strings.Join(notes, "; ")
		}
		fyne.Do(func() { inst.SetStatus(plan) })

		built.Engine.Capture(ctx, built.Devices)
	}()
}

// --- vault and prompts -----------------------------------------------------

// unlockQuiet tries the keyring and the environment and gives up silently.
//
// Startup path. It must not prompt: there is no terminal a window user is
// watching, and vaultcli.Open would block on one forever.
func (h *host) unlockQuiet() {
	if h.vaultPath == "" {
		h.refreshVault()
		return
	}
	v, err := vaultcli.OpenQuiet(h.vaultPath)
	if err != nil {
		if !errors.Is(err, vaultcli.ErrNeedsPassword) {
			log.Printf("[vault] %v", err)
		}
		h.refreshVault()
		return
	}
	h.adopt(v)
}

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// Left as "dev" otherwise, which is what an unstamped local build honestly is.
var version = "dev"

// showAbout opens the About box.
//
// The paths are here rather than in the dialog because this is the only place
// that knows which vault and which inventory this run resolved to — and those
// two answers are the first thing worth having when somebody reports that a
// credential or a session "isn't there".
// showSettings opens the settings dialog and applies what comes back.
//
// Two things happen on save and they are deliberately separate. The chrome
// variant is applied to the LIVE app, because it is the one setting a person
// can see is wrong the moment they change it. Everything else is installed as
// the new base and takes effect on the next tab: the terminal reads its font
// size and scrollback when it constructs its grid, and reaching into the open
// ones would mean re-measuring a running session's geometry underneath the
// device on the far end.
//
// The write is last and its failure is reported, not swallowed. A settings
// dialog that appears to work and silently does not persist is worse than one
// that says the disk is full.
func (h *host) showSettings() {
	ui.ShowSettings(h.win, ui.SettingsFormOptions{
		Settings: h.base,
		Paths:    h.hostPaths(),
		OnSave: func(s ui.Settings) {
			if s.AppVariant() != h.base.AppVariant() {
				ui.ApplyAppTheme(h.app, s.AppVariant())
			}
			h.base = s
			ui.SetSettings(s)

			if err := ui.SaveSettings(h.settingsPath, s); err != nil {
				dialog.ShowError(err, h.win)
			}
		},
	})
}

// hostPaths are the files this run resolved, for the settings dialog's
// read-only Paths page and for the About box. Only the host knows them: the ui
// package has no business deciding where a vault lives.
func (h *host) hostPaths() []ui.AboutDetail {
	vaultPath := h.vaultPath
	if h.vault != nil {
		vaultPath = h.vault.Path()
	}
	return []ui.AboutDetail{
		{Label: "Vault", Value: vaultPath},
		{Label: "Sessions", Value: h.sessionsPath},
		{Label: "Captures", Value: h.lastCapture.Params.StorePath},
	}
}

// flagWasSet reports whether a flag appeared on the command line, as opposed
// to holding its default. flag has no other way to tell the two apart, and the
// difference decides whether the command line overrides the settings file.
func flagWasSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func (h *host) showAbout() {
	ui.ShowAbout(h.win, ui.AboutInfo{
		Name:    ui.DefaultAppName,
		Tagline: "crawl, map and capture, from one session tree",
		Version: version,
		Details: h.hostPaths(),
	})
}

// manageVault opens the credential manager against the session's vault.
//
// It refuses rather than prompting when the vault is locked: unlocking is
// showVaultDialog's job and it is the only place a master password is asked
// for, which is what keeps that question out of every other code path.
func (h *host) manageVault() {
	if h.vault == nil {
		dialog.ShowInformation("Vault",
			"No vault is unlocked. Open Vault → Unlock first.", h.win)
		return
	}
	// refreshVault rebuilds the credential list, the lookup and the
	// toolbar together, so a credential added here is offered by the next
	// dialog that opens without anything else being told.
	ui.ShowVaultManager(h.win, h.vault, h.refreshVault)
}

// showVaultDialog is the only place a master password is ever asked for.
func (h *host) showVaultDialog() {
	if h.vault != nil {
		dialog.ShowConfirm("Vault",
			fmt.Sprintf("%s is unlocked with %d credential(s).\n\nLock it?",
				h.vault.Path(), len(h.creds)),
			func(ok bool) {
				if !ok {
					return
				}
				h.vault.Lock()
				h.vault = nil
				h.refreshVault()
			}, h.win)
		return
	}

	// Prefill with a path that EXISTS. h.vaultPath comes from -vault, and a
	// flag naming a file that is not there is the most likely reason
	// someone is opening this dialog in the first place -- making them
	// type a password before being told the file is missing is a wasted
	// round trip. DefaultPath already prefers the current app directory
	// and falls back to the legacy one, so it finds a vault that moved.
	start := h.vaultPath
	if _, err := os.Stat(start); err != nil {
		if def := vaultcli.DefaultPath(); def != "" {
			if _, err := os.Stat(def); err == nil {
				start = def
			}
		}
	}

	path := widget.NewEntry()
	path.SetText(start)
	path.SetPlaceHolder(vaultcli.DefaultPath())

	found := widget.NewLabel("")
	found.Wrapping = fyne.TextWrapWord
	checkPath := func(p string) {
		p = ui.ExpandHome(p)
		if p == "" {
			p = vaultcli.DefaultPath()
		}
		if _, err := os.Stat(p); err != nil {
			found.SetText("no vault file at that path yet")
			return
		}
		found.SetText("vault file found")
	}
	path.OnChanged = checkPath
	checkPath(start)

	pass := widget.NewPasswordEntry()

	// Off by default. Filing a master password in the OS keyring is a
	// decision, and one that outlives this window -- it is what makes every
	// later start unlock silently, which is exactly why it should be
	// deliberate rather than a default nobody noticed.
	remember := widget.NewCheck("Remember in the OS keyring", nil)

	items := []*widget.FormItem{
		widget.NewFormItem("Vault", path),
		widget.NewFormItem("", found),
		widget.NewFormItem("Master password", pass),
		widget.NewFormItem("", remember),
	}

	d := dialog.NewForm("Unlock vault", "Unlock", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		p := ui.ExpandHome(path.Text)
		if p == "" {
			p = vaultcli.DefaultPath()
		}
		v, err := vaultcli.OpenWith(p, pass.Text)
		if err != nil {
			// Distinguishable on purpose: a wrong password and a
			// missing file need different next actions, and the
			// error already says which it was.
			dialog.ShowError(err, h.win)
			return
		}
		h.vaultPath = p
		h.adopt(v)
		if remember.Checked {
			if err := vaultcli.KeyringSet(p, pass.Text); err != nil {
				dialog.ShowError(fmt.Errorf("unlocked, but the keyring refused it: %w", err), h.win)
			}
		}
	}, h.win)
	d.Resize(fyne.NewSize(560, 300))
	d.Show()
}

// adopt takes ownership of an unlocked vault and rebuilds everything derived
// from it.
func (h *host) adopt(v *vault.Vault) {
	if h.vault != nil && h.vault != v {
		h.vault.Lock()
	}
	h.vault = v
	h.vaultPath = v.Path()
	log.Printf("[vault] %s unlocked, %d credential(s)", v.Path(), len(v.Names()))
	h.refreshVault()
}

// refreshVault rebuilds the credential list, the dialer lookup and the toolbar
// button from whatever h.vault currently is.
//
// One function so the three can never disagree -- a picker offering names from
// a vault that has since been locked is worse than an empty picker, because
// the failure arrives at connect time instead of at click time.
func (h *host) refreshVault() {
	if h.vault == nil {
		h.creds = nil
		h.lookup = nil
		h.defaultCred = ""
		if h.vaultBtn != nil {
			h.vaultBtn.SetIcon(theme.LoginIcon())
			h.vaultBtn.SetText("Vault locked")
		}
		return
	}

	v := h.vault
	h.creds = v.Names()
	h.defaultCred = v.DefaultName()
	h.lookup = func(ref string) (sessiondial.Credential, error) {
		// An EMPTY ref asks what this store uses when a session names
		// nothing. It is not a lookup failure: "" names nothing, so it
		// cannot be missing, and a session that says nothing about auth
		// is the ordinary state of every node produced by a map import.
		if strings.TrimSpace(ref) == "" {
			c, ok := v.Default()
			if !ok {
				return sessiondial.Credential{}, nil
			}
			return dialCredential(c), nil
		}
		c, err := v.Get(ref)
		if err != nil {
			return sessiondial.Credential{}, err
		}
		return dialCredential(c), nil
	}
	if h.vaultBtn != nil {
		h.vaultBtn.SetIcon(theme.ConfirmIcon())
		h.vaultBtn.SetText(fmt.Sprintf("Vault %s", filepath.Base(v.Path())))
	}
}

// dialCredential is the ONE vault-to-dial conversion.
//
// One function because there are two ways a credential reaches a connection --
// named by the session, or standing behind a blank field as the store default
// -- and two conversions would be two places for the auth type or a passphrase
// to be dropped from one path and not the other.
func dialCredential(c vault.Credential) sessiondial.Credential {
	return sessiondial.Credential{
		Username:      c.Username,
		AuthType:      authTypeName(c),
		Password:      c.Password,
		KeyPath:       c.KeyPath,
		KeyPassphrase: c.KeyPassphrase,
	}
}

// runVaultPath is the path a run should record, and it is empty unless the
// vault is actually open.
//
// That emptiness is load-bearing: Build treats an empty VaultPath as "use the
// static credentials from the form", which is the honest behaviour for a
// locked vault. A path with no open vault behind it would send Build looking
// for a master password it cannot ask for.
func (h *host) runVaultPath() string {
	if h.vault == nil {
		return ""
	}
	return h.vaultPath
}

func authTypeName(c vault.Credential) string {
	switch c.Method() {
	case vault.AuthPublicKey:
		return sessions.AuthPublicKey
	case vault.AuthPassword:
		return sessions.AuthPassword
	default:
		return ""
	}
}

// promptHostKey asks in the GUI rather than on stderr: by the time this fires
// there is a window on screen and nobody is watching the terminal the app was
// launched from. Called on the dial goroutine, so it hops to the UI goroutine
// to show the dialog and blocks on the answer.
func (h *host) promptHostKey(hostname string, remote net.Addr, key ssh.PublicKey) (bool, error) {
	answer := make(chan bool, 1)
	msg := fmt.Sprintf("%s (%s)\n\n%s\n%s\n\nAccept and remember this key?",
		hostname, remote, key.Type(), ssh.FingerprintSHA256(key))
	fyne.Do(func() {
		dialog.ShowConfirm("Unknown host key", msg, func(ok bool) { answer <- ok }, h.win)
	})
	select {
	case ok := <-answer:
		return ok, nil
	case <-time.After(60 * time.Second):
		// A prompt nobody answers must resolve to no. Timing out into
		// yes would make the policy meaningless in exactly the case
		// where it matters.
		return false, fmt.Errorf("host key prompt timed out")
	}
}

// promptSecret answers password and keyboard-interactive challenges the node
// did not supply material for.
func (h *host) promptSecret(prompt string, echo bool) (string, error) {
	answer := make(chan string, 1)
	fyne.Do(func() {
		field := widget.NewPasswordEntry()
		if echo {
			field = widget.NewEntry()
		}
		field.SetPlaceHolder(strings.TrimSpace(prompt))
		d := dialog.NewForm("Authentication", "Send", "Cancel",
			[]*widget.FormItem{widget.NewFormItem(strings.TrimSpace(prompt), field)},
			func(ok bool) {
				if ok {
					answer <- field.Text
					return
				}
				answer <- ""
			}, h.win)
		d.Resize(fyne.NewSize(420, 200))
		d.Show()
	})
	select {
	case s := <-answer:
		return s, nil
	case <-time.After(120 * time.Second):
		return "", fmt.Errorf("authentication prompt timed out")
	}
}

// listPorts feeds the serial dropdown. On macOS prefer a /dev/cu.* entry over
// the matching /dev/tty.*: the tty device blocks on open until carrier is
// asserted, which a console cable with no modem control never does, so the app
// appears to hang.
func listPorts() []string {
	if ports, err := serialx.ListDetailed(); err == nil {
		out := make([]string, 0, len(ports))
		for _, p := range ports {
			out = append(out, p.Name)
		}
		return out
	}
	names, err := serialx.List()
	if err != nil {
		log.Printf("[serial] listing ports: %v", err)
		return nil
	}
	return names
}
