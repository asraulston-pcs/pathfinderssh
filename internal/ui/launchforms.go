// internal/ui/launchforms.go
//
// The two parameter dialogs the shell's Launch buttons open.
//
// A terminal already had one -- SessionForm, which edits a sessions.Node --
// and this is the same idea for the other two applets: data in, data out. The
// dialog produces Params and nothing else. It cannot build a crawler, open a
// vault, or start anything, for the same reason SessionForm cannot connect.
//
// # Why a form and not just flags
//
// A dialog that opens empty every time is slower than the command line it
// replaces. These open on the values last used in this process, so the second
// crawl of a session is one click. Persisting them across runs is
// crawlrun.Profiles' job and is not wired here.
//
// # Validation
//
// Params.Validate returns every bad field rather than the first, so the status
// line can say all of them at once. A confirm dialog cannot refuse to dismiss,
// so a failed validation re-opens the same content object -- which still holds
// what was typed, because the widgets were never rebuilt.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/capturerun"
	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
)

// LaunchAuth is the credential material a params struct deliberately does not
// carry. It is a plain struct here rather than the dialer's own type so this
// file does not import a dialer.
type LaunchAuth struct {
	Username string
	Password string
	KeyPath  string
}

// CrawlLaunch is everything the crawl dialog collects.
type CrawlLaunch struct {
	Params crawlrun.Params
	Auth   LaunchAuth

	// MapPath, SaveRun and LastRun are the file side of a run: where the
	// topology goes, where this run is recorded for the next comparison,
	// and which previous run to compare against.
	MapPath string
	SaveRun string
	LastRun string

	Verbose bool
}

// CaptureLaunch is everything the capture dialog collects.
type CaptureLaunch struct {
	Params capturerun.Params
	Auth   LaunchAuth

	Verbose bool
}

// ShowCrawlDialog collects crawl parameters and calls onRun with them.
//
// prev seeds the fields, so passing back the last launch makes the second crawl
// a one-click repeat. knownTypes is not needed here; capture's dialog takes it.
func ShowCrawlDialog(w fyne.Window, prev CrawlLaunch, onRun func(CrawlLaunch)) {
	p := prev.Params
	if p.Depth == 0 {
		p = crawlrun.Defaults()
	}

	seeds := multiline("one per line, or comma separated")
	seeds.SetText(strings.Join(p.Seeds, "\n"))

	depth := numEntry(p.Depth)
	conc := numEntry(p.Concurrency)
	timeout := entryWith(p.Timeout.String())

	domains := entryWith(strings.Join(p.Domains, ", "))
	domains.SetPlaceHolder("lab.local")
	allowDom := entryWith(strings.Join(p.AllowDomains, ", "))
	allowDom.SetPlaceHolder("restrict which neighbors are dialed at all")
	exclude := entryWith(strings.Join(p.Exclude, ", "))
	exclude.SetPlaceHolder("substrings matched against platform, hostname, sysname")

	credTags := entryWith(strings.Join(p.CredTags, ", "))
	user := entryWith(prev.Auth.Username)
	pass := widget.NewPasswordEntry()
	pass.SetText(prev.Auth.Password)
	keyPath := entryWith(prev.Auth.KeyPath)

	// TOFU is the default and there is no third option. Insecure is the
	// only mode that also stops checking a key that CHANGED, and that is a
	// decision someone should type on a command line rather than tick in a
	// dialog next to the concurrency box.
	hostKeys := widget.NewSelect([]string{"TOFU (trust on first contact)", "Strict"}, nil)
	if p.HostKeys == crawlrun.HostKeyStrict {
		hostKeys.SetSelectedIndex(1)
	} else {
		hostKeys.SetSelectedIndex(0)
	}
	knownHosts := entryWith(p.KnownHostsPath)

	legacy := widget.NewCheck("Legacy KEX and ciphers", nil)
	legacy.SetChecked(p.Legacy)
	trustUni := widget.NewCheck("Trust one-sided link claims between discovered devices", nil)
	trustUni.SetChecked(p.TrustUnidirectional)
	verbose := widget.NewCheck("Log progress to stderr", nil)
	verbose.SetChecked(prev.Verbose)

	// Placeholders name a concrete file rather than describing the field.
	// These three are the durable half of a crawl and they are the easiest
	// thing in the dialog to leave blank -- the run then looks identical
	// and produces nothing.
	mapPath := entryWith(prev.MapPath)
	mapPath.SetPlaceHolder("blank = no map written, e.g. ~/crawl-map.json")
	saveRun := entryWith(prev.SaveRun)
	saveRun.SetPlaceHolder("blank = not recorded, e.g. ~/last-run.json")
	lastRun := entryWith(prev.LastRun)
	lastRun.SetPlaceHolder("blank = no comparison, e.g. ~/last-run.json")

	status := statusLabel()

	// Host keys and Legacy sit here for the same reason they do in the
	// capture dialog below: both decide whether a run connects at all, and
	// a control that decides that belongs beside the devices it applies
	// to. Filed under Output and Credentials they read as things to leave
	// alone, which is what happened.
	crawlTab := formOf(
		"Seeds", tall(seeds, 110),
		"Depth", depth,
		"Concurrency", conc,
		"Command timeout", timeout,
		"Domain suffixes", domains,
		"Allow domains", allowDom,
		"Exclude", exclude,
		"Host keys", hostKeys,
		"", legacy,
	)
	// No vault field. The vault is unlocked once, by the host, and a dialog
	// has nothing useful to say about it: a path typed here would be opened
	// a second time by Build, on the run's own goroutine, with nowhere to
	// ask for a master password.
	authTab := formOf(
		"Credential tags", credTags,
		"Username", user,
		"Password", pass,
		"Key file", pathRow(w, keyPath, pathOpenFile, ""),
		"known_hosts", pathRow(w, knownHosts, pathOpenFile, ""),
	)
	outTab := formOf(
		"Map output", pathRow(w, mapPath, pathOutput, "map.json"),
		"Save run", pathRow(w, saveRun, pathOutput, "last-run.json"),
		"Compare with", pathRow(w, lastRun, pathOpenFile, ""),
		"", trustUni,
		"", verbose,
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Crawl", crawlTab),
		container.NewTabItem("Credentials", authTab),
		container.NewTabItem("Output", outTab),
	)
	content := container.NewBorder(nil, status, nil, nil, tabs)

	var show func()
	show = func() {
		d := dialog.NewCustomConfirm("New crawl", "Start", "Cancel", content, func(ok bool) {
			if !ok {
				return
			}
			out := CrawlLaunch{
				Params:  crawlrun.Defaults(),
				Auth:    LaunchAuth{Username: user.Text, Password: pass.Text, KeyPath: ExpandHome(keyPath.Text)},
				MapPath: ExpandHome(mapPath.Text),
				SaveRun: ExpandHome(saveRun.Text),
				LastRun: ExpandHome(lastRun.Text),
				Verbose: verbose.Checked,
			}
			out.Params.Seeds = crawlrun.ParseSeeds(seeds.Text)
			out.Params.Depth = atoiOr(depth.Text, out.Params.Depth)
			out.Params.Concurrency = atoiOr(conc.Text, out.Params.Concurrency)
			out.Params.Timeout = durationOr(timeout.Text, out.Params.Timeout)
			out.Params.Domains = crawlrun.ParseSeeds(domains.Text)
			out.Params.AllowDomains = crawlrun.ParseSeeds(allowDom.Text)
			out.Params.Exclude = crawlrun.ParseSeeds(exclude.Text)
			// Carried from prev, not from a field: the host owns it.
			out.Params.VaultPath = p.VaultPath
			out.Params.CredTags = crawlrun.ParseSeeds(credTags.Text)
			out.Params.KnownHostsPath = ExpandHome(knownHosts.Text)
			out.Params.Legacy = legacy.Checked
			out.Params.TrustUnidirectional = trustUni.Checked
			if hostKeys.SelectedIndex() == 1 {
				out.Params.HostKeys = crawlrun.HostKeyStrict
			}

			var msgs []string
			for _, e := range out.Params.Validate() {
				msgs = append(msgs, e.Error())
			}
			// Paths are checked here and not in Params.Validate,
			// which is shared with the CLI and must not touch the
			// filesystem. A wrong path used to survive validation
			// and produce a run that wrote nothing.
			for _, e := range []error{
				checkOutputPath("map output", out.MapPath),
				checkOutputPath("save run", out.SaveRun),
				checkInputPath("compare with", out.LastRun),
				checkInputPath("key file", out.Auth.KeyPath),
			} {
				if e != nil {
					msgs = append(msgs, e.Error())
				}
			}
			if len(msgs) > 0 {
				status.SetText("⚠  " + strings.Join(msgs, " · "))
				// A confirm dialog cannot refuse to dismiss, so
				// re-open it. The content object is the same one,
				// so nothing typed is lost.
				show()
				return
			}
			status.SetText("")
			onRun(out)
		}, w)
		d.Resize(fyne.NewSize(700, 560))
		d.Show()
	}
	show()
}

// intersect keeps the members of want that appear in have, in have's order.
//
// A saved selection can name a type this build no longer has — a spec removed
// between releases, or a profile written by a newer one. Handing that to a
// check group as a selection would either be ignored silently or show a tick
// against nothing; dropping it means the dialog shows what it will actually
// run.
func intersect(want, have []string) []string {
	var out []string
	for _, h := range have {
		for _, w := range want {
			if strings.EqualFold(h, w) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

// ShowCaptureDialog collects capture parameters and calls onRun with them.
//
// knownTypes comes from the caller rather than from capturedial, so this file
// does not import an assembly package. An empty list leaves the field free
// text, which still works.
func ShowCaptureDialog(w fyne.Window, prev CaptureLaunch, knownTypes []string, onRun func(CaptureLaunch)) {
	p := prev.Params
	if p.Concurrency == 0 {
		p = capturerun.Defaults()
	}

	devices := multiline("one per line, or comma separated")
	devices.SetText(strings.Join(p.Devices, "\n"))
	deviceFile := entryWith(p.DeviceFile)
	deviceFile.SetPlaceHolder("or a file of device names")

	// The session tree as a device source. Sessions are selected by glob
	// rather than picked one at a time because the point of pointing a
	// capture at the inventory is to say "every aggregation switch" once
	// and have it keep meaning that as the inventory grows.
	sessionFile := entryWith(p.SessionFile)
	sessionFile.SetPlaceHolder("or a session file to select from")
	match := entryWith(strings.Join(p.Match, ", "))
	match.SetPlaceHolder("agg*, *-sw-*   (matches a session name or host)")

	// Types is a FIXED set, not free text. capture.Builtin() lives in Go
	// source by design — the read-only allowlist test cannot cover a file
	// somebody edits at runtime — so anything typed here that is not a
	// builtin can only ever come back as a validation error. A check group
	// offers exactly what exists and makes the field unable to be wrong.
	//
	// A check group and not a dropdown: a run is devices x types, so
	// picking one type would be picking the wrong thing. Nothing checked
	// is legal and means the Build layer's default set — the default lives
	// there, and repeating it here is how the two come to disagree.
	//
	// The entry survives for the case the doc above describes: an empty
	// knownTypes leaves the field free text, which still works.
	types := entryWith(strings.Join(p.Types, ", "))
	typeChoice := widget.NewCheckGroup(knownTypes, nil)
	typeChoice.SetSelected(intersect(p.Types, knownTypes))
	var typesField fyne.CanvasObject = types
	if len(knownTypes) > 0 {
		types.SetPlaceHolder(strings.Join(knownTypes, ", "))
		typesField = typeChoice
	}

	store := entryWith(p.StorePath)
	conc := numEntry(p.Concurrency)
	expConc := numEntry(p.ExpensiveConcurrency)
	timeout := entryWith(p.Timeout.String())
	domains := entryWith(strings.Join(p.Domains, ", "))

	credTags := entryWith(strings.Join(p.CredTags, ", "))
	user := entryWith(prev.Auth.Username)
	pass := widget.NewPasswordEntry()
	pass.SetText(prev.Auth.Password)
	keyPath := entryWith(prev.Auth.KeyPath)

	// Strict is the default here and TOFU is the opt-in, which is the
	// opposite of crawl: a crawl meets devices it has never seen, a capture
	// works from a list of devices someone already administers.
	hostKeys := widget.NewSelect([]string{"Strict", "TOFU (trust on first contact)"}, nil)
	if p.HostKeys == capturerun.HostKeyTOFU {
		hostKeys.SetSelectedIndex(1)
	} else {
		hostKeys.SetSelectedIndex(0)
	}
	knownHosts := entryWith(p.KnownHostsPath)

	legacy := widget.NewCheck("Legacy KEX and ciphers", nil)
	legacy.SetChecked(p.Legacy)
	verbose := widget.NewCheck("Log progress to stderr", nil)
	verbose.SetChecked(prev.Verbose)

	status := statusLabel()

	// Host keys and Legacy live on this tab rather than under Credentials
	// and Limits. Neither is a limit and neither is tuning: on a mixed
	// estate Legacy is the difference between the old half of the fleet
	// answering and none of it answering, and Host keys is the difference
	// between a device nobody has met before being reachable and not. A
	// control that decides whether a run works at all belongs beside the
	// devices it applies to; filed elsewhere they read as things to leave
	// alone, which is exactly what happened to both.
	//
	// known_hosts stays on Credentials. It is a path, and a path is a
	// detail of the policy rather than the decision itself.
	runTab := formOf(
		"Devices", tall(devices, 110),
		"Device file", pathRow(w, deviceFile, pathOpenFile, ""),
		"Session file", pathRow(w, sessionFile, pathOpenFile, ""),
		"Match sessions", match,
		"Types", typesField,
		"Store", pathRow(w, store, pathFolder, ""),
		"Domain suffixes", domains,
		"Host keys", hostKeys,
		"", legacy,
	)
	limitsTab := formOf(
		"Concurrency", conc,
		"Expensive concurrency", expConc,
		"Command timeout", timeout,
		"", verbose,
	)
	// No vault field — see the crawl dialog above.
	authTab := formOf(
		"Credential tags", credTags,
		"Username", user,
		"Password", pass,
		"Key file", pathRow(w, keyPath, pathOpenFile, ""),
		"known_hosts", pathRow(w, knownHosts, pathOpenFile, ""),
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Capture", runTab),
		container.NewTabItem("Limits", limitsTab),
		container.NewTabItem("Credentials", authTab),
	)
	content := container.NewBorder(nil, status, nil, nil, tabs)

	var show func()
	show = func() {
		d := dialog.NewCustomConfirm("New capture", "Start", "Cancel", content, func(ok bool) {
			if !ok {
				return
			}
			out := CaptureLaunch{
				Params:  capturerun.Defaults(),
				Auth:    LaunchAuth{Username: user.Text, Password: pass.Text, KeyPath: ExpandHome(keyPath.Text)},
				Verbose: verbose.Checked,
			}
			out.Params.Devices = capturerun.ParseDevices(devices.Text)
			out.Params.DeviceFile = ExpandHome(deviceFile.Text)
			out.Params.SessionFile = ExpandHome(sessionFile.Text)
			out.Params.Match = capturerun.ParseDevices(match.Text)
			out.Params.Types = capturerun.ParseDevices(types.Text)
			if len(knownTypes) > 0 {
				out.Params.Types = append([]string(nil), typeChoice.Selected...)
			}
			out.Params.StorePath = ExpandHome(store.Text)
			out.Params.Domains = capturerun.ParseDevices(domains.Text)
			out.Params.Concurrency = atoiOr(conc.Text, out.Params.Concurrency)
			out.Params.ExpensiveConcurrency = atoiOr(expConc.Text, out.Params.ExpensiveConcurrency)
			out.Params.Timeout = durationOr(timeout.Text, out.Params.Timeout)
			out.Params.VaultPath = p.VaultPath
			out.Params.CredTags = capturerun.ParseDevices(credTags.Text)
			out.Params.KnownHostsPath = ExpandHome(knownHosts.Text)
			out.Params.Legacy = legacy.Checked
			if hostKeys.SelectedIndex() == 1 {
				out.Params.HostKeys = capturerun.HostKeyTOFU
			}

			// ValidateAgainst also rejects a type name the build
			// would not recognise, which is the field most likely
			// to be mistyped and the one whose mistake would
			// otherwise surface as an empty run.
			errs := out.Params.Validate()
			if len(knownTypes) > 0 {
				errs = out.Params.ValidateAgainst(knownTypes)
			}
			var msgs []string
			for _, e := range errs {
				msgs = append(msgs, e.Error())
			}
			for _, e := range []error{
				checkInputPath("device file", out.Params.DeviceFile),
				checkInputPath("session file", out.Params.SessionFile),
				checkInputPath("key file", out.Auth.KeyPath),
			} {
				if e != nil {
					msgs = append(msgs, e.Error())
				}
			}
			if len(msgs) > 0 {
				status.SetText("⚠  " + strings.Join(msgs, " · "))
				show()
				return
			}
			status.SetText("")
			onRun(out)
		}, w)
		d.Resize(fyne.NewSize(700, 560))
		d.Show()
	}
	show()
}

// --- path fields ----------------------------------------------------------

// pathRow is an entry with a Browse button beside it.
//
// Typing a path by hand is where this dialog was losing people. Worse, an
// output path that named a DIRECTORY produced a run that looked completely
// normal and wrote nothing, because the write failure and the "you left it
// blank" case were indistinguishable from the outside.
//
// kind selects the picker. Fyne's ShowFileSave CREATES the file it returns, so
// an output path uses the folder picker plus a filename instead — a save
// target should not exist until something is written to it.
type pathKind int

const (
	pathOpenFile pathKind = iota // an existing file to read
	pathFolder                   // a directory
	pathOutput                   // a file to write: pick the folder, keep the name
)

func pathRow(w fyne.Window, e *widget.Entry, kind pathKind, defaultName string) fyne.CanvasObject {
	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		switch kind {
		case pathOpenFile:
			dialog.ShowFileOpen(func(r fyne.URIReadCloser, err error) {
				if err != nil || r == nil {
					return
				}
				defer r.Close()
				e.SetText(r.URI().Path())
			}, w)
		default:
			dialog.ShowFolderOpen(func(list fyne.ListableURI, err error) {
				if err != nil || list == nil {
					return
				}
				dir := list.Path()
				if kind == pathFolder {
					e.SetText(dir)
					return
				}
				// Keep whatever filename is already typed, so
				// browsing changes the directory without
				// discarding the name.
				name := filepath.Base(strings.TrimSpace(e.Text))
				if name == "" || name == "." || name == string(filepath.Separator) {
					name = defaultName
				}
				e.SetText(filepath.Join(dir, name))
			}, w)
		}
	})
	browse.Importance = widget.LowImportance
	return container.NewBorder(nil, nil, nil, browse, e)
}

// checkOutputPath rejects the mistake that used to be silent: a path that names
// an existing DIRECTORY. Writing to it fails, and before the failure was
// reported at all it produced a run that looked clean and saved nothing.
//
// A path whose parent does not exist is fine — the writer creates it.
func checkOutputPath(label, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return fmt.Errorf("%s is a directory — add a filename, e.g. %s",
			label, filepath.Join(path, "map.json"))
	}
	return nil
}

// checkInputPath rejects a named input that is not there, before a run starts
// rather than partway through it.
func checkInputPath(label, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s: %s does not exist", label, path)
	}
	return nil
}

// --- small builders -------------------------------------------------------
//
// These exist so the two dialogs above read as a list of fields. A form row
// whose content is a composite is the first thing to suspect when a Fyne dialog
// will not fit its window, so every row here is one widget.

func entryWith(text string) *widget.Entry {
	e := widget.NewEntry()
	e.SetText(text)
	return e
}

func numEntry(n int) *widget.Entry {
	e := widget.NewEntry()
	if n > 0 {
		e.SetText(strconv.Itoa(n))
	}
	return e
}

func multiline(placeholder string) *widget.Entry {
	e := widget.NewMultiLineEntry()
	e.SetPlaceHolder(placeholder)
	e.Wrapping = fyne.TextWrapOff
	return e
}

// tall gives a multi-line entry a usable height. A form layout sizes rows to
// their minimum, and a MultiLineEntry's minimum is a couple of lines.
func tall(obj fyne.CanvasObject, h float32) fyne.CanvasObject {
	scroll := container.NewScroll(obj)
	scroll.SetMinSize(fyne.NewSize(0, h))
	return scroll
}

func statusLabel() *widget.Label {
	l := widget.NewLabel("")
	l.Wrapping = fyne.TextWrapWord
	return l
}

// formOf takes alternating label/field pairs. An empty label produces a row
// with no caption, which is what a checkbox wants.
func formOf(pairs ...any) fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, len(pairs))
	for i := 0; i+1 < len(pairs); i += 2 {
		label, _ := pairs[i].(string)
		field, _ := pairs[i+1].(fyne.CanvasObject)
		if field == nil {
			continue
		}
		objs = append(objs, widget.NewLabel(label), field)
	}
	return container.NewVScroll(container.New(layout.NewFormLayout(), objs...))
}

func atoiOr(s string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return fallback
}

func durationOr(s string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
		return d
	}
	return fallback
}
