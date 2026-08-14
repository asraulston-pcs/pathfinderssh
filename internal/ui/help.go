// internal/ui/help.go
//
// Opening the help page, and the ? button that dialogs put on themselves.
//
// internal/help renders the page and knows nothing about a toolkit. This file
// is the other half: it turns a topic into a browser window.
//
// # Why the configuration is package state
//
// A dialog deep inside this package needs two facts to open help -- where the
// application keeps its files, and what version this build is -- and neither is
// anything a form should be told. Threading them through every constructor
// would change ShowCrawlDialog, ShowCaptureDialog, SessionForm and
// SettingsForm to carry a field none of them use for anything else.
//
// So the host sets it once at startup. The honest cost: this is mutable
// package state, and package state is normally how two things come to disagree
// about a value. It is tolerable here because there is exactly one application
// per process, exactly one writer, and it is written before any window opens.
// If a second writer ever appears, thread it through instead.
//
// # A missing configuration must be loud, not quiet
//
// A help BUTTON with no configuration hides itself: a ? that does nothing is
// worse than no ?, because the person clicking it concludes the program is
// broken rather than that the feature is absent.
//
// ShowHelp cannot use that rule, and the first version of this file wrongly
// did. A menu item is built by the host unconditionally, so a silent return
// produces a Help menu whose entries do nothing at all -- no window, no error,
// no file on disk, nothing to search for. It has to say what is wrong instead.
package ui

import (
	"errors"
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/help"
)

// HelpConfig is what the host knows and this package does not.
type HelpConfig struct {
	// Dir is where the rendered page is written. Blank means
	// <GetAppHome()>/help, which is what every normal build wants -- the
	// field exists for a test that needs a temporary directory.
	Dir string

	// Version stamps the page. A build whose stamp differs from the file
	// on disk re-renders it; an empty version re-renders every time,
	// which is what a developer build wants.
	Version string
}

// helpCfg is set once by SetHelp. helpOn is separate from a blank Dir because
// a blank Dir is a legal configuration meaning "the usual place" -- without the
// flag there would be no way to distinguish that from never having been called,
// and the difference decides whether the buttons appear at all.
var (
	helpCfg HelpConfig
	helpOn  bool
)

// SetHelp configures the help system. Call once, before any window opens.
func SetHelp(c HelpConfig) {
	helpCfg = c
	helpOn = true
}

// HelpAvailable reports whether help can be opened.
func HelpAvailable() bool { return helpOn }

// helpDir is where the rendered page goes.
func helpDir() string {
	if helpCfg.Dir != "" {
		return helpCfg.Dir
	}
	return filepath.Join(GetAppHome(), "help")
}

// ShowHelp renders the page if needed and opens the browser at topic.
//
// An empty topic opens at the top of the page. Errors are reported to the
// person rather than swallowed: failing to open help is not the same kind of
// nothing as a missing logo -- they asked for it.
func ShowHelp(w fyne.Window, topic help.Topic) {
	if !HelpAvailable() {
		// Not a user error. Say plainly what is missing rather than
		// pretending the click did not happen.
		dialog.ShowError(errors.New(
			"help is not configured: the application did not call "+
				"ui.SetHelp before opening a window"), w)
		return
	}
	path, err := help.Ensure(helpDir(), helpCfg.Version)
	if err != nil {
		showHelpError(w, err)
		return
	}
	u, err := help.URL(path, topic)
	if err != nil {
		showHelpError(w, err)
		return
	}
	if err := fyne.CurrentApp().OpenURL(u); err != nil {
		// The page exists and we know where it is, so say so. On a
		// machine with no browser association -- or one where the
		// browser cannot reach this filesystem at all -- that path is
		// the only useful part of the message.
		dialog.ShowInformation("Open this in a browser", u.String(), w)
	}
}

func showHelpError(w fyne.Window, err error) {
	if w == nil {
		return
	}
	dialog.ShowError(fmt.Errorf("could not open help: %w", err), w)
}

// HelpButton returns a small ? that opens help at topic, or nil when help is
// not configured.
//
// Callers must handle nil. Adding nil to a container is a panic, so the
// idiom is:
//
//	if b := HelpButton(w, help.TopicCrawl); b != nil {
//	    row.Add(b)
//	}
func HelpButton(w fyne.Window, topic help.Topic) *widget.Button {
	if !HelpAvailable() {
		return nil
	}
	b := widget.NewButtonWithIcon("", theme.HelpIcon(), func() {
		ShowHelp(w, topic)
	})
	b.Importance = widget.LowImportance
	return b
}

// helpRow puts a help button at the trailing edge of a dialog's status line,
// which is where every launch form already has spare room.
//
// Returns content unchanged when help is unavailable, so a caller does not
// have to branch.
func helpRow(w fyne.Window, topic help.Topic, content fyne.CanvasObject) fyne.CanvasObject {
	b := HelpButton(w, topic)
	if b == nil {
		return content
	}
	return container.NewBorder(nil, nil, nil, b, content)
}

// HelpPath is the file the help page is rendered to, for the Paths page.
//
// It names where the page WILL be written, which is the question that page
// answers everywhere else on it -- not whether it happens to exist yet.
func HelpPath() string {
	if !helpOn {
		return ""
	}
	return filepath.Join(helpDir(), help.FileName)
}
