// internal/ui/settingsdialog.go
//
// The application settings dialog. Layout only: every rule about what a value
// may be lives in settingsfields.go, which has no toolkit in it and is tested.
//
// # What is on it, and why the list is short
//
// This edits the Settings struct -- the values that hold for the application
// regardless of which session is in front. It is deliberately not a control
// panel for everything the program can be told:
//
//   - per-session overrides belong to the session form, which already has them
//   - crawl and capture parameters belong to their launch dialogs, because a
//     run's settings are part of that run
//   - credentials belong to the vault manager
//
// A setting that appears in two places is a setting that will disagree with
// itself, and the person looking at the one that did nothing has no way to
// know which one won.
//
// # The Paths page is not a setting
//
// It is read-only, and it is here because "my credentials aren't there" and
// "where did my transcript go" are the same question -- which file did THIS run
// actually resolve to. The host supplies the answers; this package has no
// opinion about where a vault lives.
//
// # Follows the view contract
//
// Constructed after app.New(). Content() returns a CanvasObject so the host
// decides placement. Callbacks out, imports in.
package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// appThemeChoices are the two chrome variants. There is no "system": see the
// AppVariant doc in settings.go.
var appThemeChoices = []string{string(AppDark), string(AppLight)}

// SettingsFormOptions is everything the dialog needs that it cannot work out.
type SettingsFormOptions struct {
	// Settings is what the form opens on.
	Settings Settings

	// Paths are shown read-only on the Paths page. The host fills them
	// because it is the only thing that knows which files this run
	// resolved to.
	Paths []AboutDetail

	// OnCancel and OnSave are the two outcomes. A nil OnSave hides the
	// Save button rather than showing one that does nothing.
	OnCancel func()
	OnSave   func(Settings)
}

// SettingsForm is the settings editor. Build it with NewSettingsForm.
type SettingsForm struct {
	opts SettingsFormOptions

	appTheme   *widget.Select
	termTheme  *widget.Select
	fontSize   *widget.Select
	scrollback *widget.Entry

	pasteDelay  *widget.Entry
	consoleBaud *widget.Select
	pasteWarn   *widget.Entry

	logDir     *widget.Entry
	logStamped *widget.Check

	antiIdle      *widget.Check
	antiIdleSecs  *widget.Entry
	antiIdleKeys  *widget.Select
	antiIdleGroup *fyne.Container

	labelToThemeKey map[string]string
	themeKeyToLabel map[string]string

	status  *widget.Label
	tabs    *container.AppTabs
	content fyne.CanvasObject
}

// NewSettingsForm builds the editor over the supplied settings.
func NewSettingsForm(opts SettingsFormOptions) *SettingsForm {
	f := &SettingsForm{opts: opts}
	f.build()
	f.SetSettings(opts.Settings)
	return f
}

// Content is the form for the host to place.
func (f *SettingsForm) Content() fyne.CanvasObject { return f.content }

// SetSettings loads settings into the widgets.
func (f *SettingsForm) SetSettings(s Settings) {
	v := SettingsFieldsOf(s)

	f.appTheme.SetSelected(v.AppTheme)
	f.termTheme.SetSelected(f.themeLabelFor(v.TerminalTheme))
	f.fontSize.SetSelected(v.FontSize)
	f.scrollback.SetText(v.ScrollbackLines)

	f.pasteDelay.SetText(v.PasteLineDelayMs)
	f.consoleBaud.SetSelected(v.PasteConsoleBaud)
	f.pasteWarn.SetText(v.PasteWarnLines)

	f.logDir.SetText(v.LogDirectory)
	f.logStamped.SetChecked(v.TimestampLogs)

	f.antiIdle.SetChecked(v.AntiIdleEnabled)
	f.antiIdleSecs.SetText(v.AntiIdleIntervalSec)
	f.antiIdleKeys.SetSelected(v.AntiIdleKeystroke)
	f.applyAntiIdleState()
}

// Settings reads the form. It reports false and leaves the status line
// explaining why when a value was refused.
func (f *SettingsForm) Settings() (Settings, bool) {
	out, errs := f.read().Apply(f.opts.Settings)
	if len(errs) > 0 {
		f.status.SetText(SettingsProblems(errs))
		return out, false
	}
	f.status.SetText("")
	return out, true
}

func (f *SettingsForm) read() SettingsFields {
	return SettingsFields{
		AppTheme:      f.appTheme.Selected,
		TerminalTheme: f.themeKeyFor(f.termTheme.Selected),
		FontSize:      f.fontSize.Selected,

		ScrollbackLines:  f.scrollback.Text,
		PasteLineDelayMs: f.pasteDelay.Text,
		PasteConsoleBaud: f.consoleBaud.Selected,
		PasteWarnLines:   f.pasteWarn.Text,

		LogDirectory:  f.logDir.Text,
		TimestampLogs: f.logStamped.Checked,

		AntiIdleEnabled:     f.antiIdle.Checked,
		AntiIdleIntervalSec: f.antiIdleSecs.Text,
		AntiIdleKeystroke:   f.antiIdleKeys.Selected,
	}
}

func (f *SettingsForm) build() {
	f.appTheme = widget.NewSelect(appThemeChoices, nil)

	labels, labelToKey, keyToLabel := ThemeMenuData()
	f.labelToThemeKey, f.themeKeyToLabel = labelToKey, keyToLabel
	f.termTheme = widget.NewSelect(labels, nil)

	// The application font size list is the same range the session form
	// offers, minus its "(inherit)" entry: there is nothing above this to
	// inherit from.
	f.fontSize = widget.NewSelect(applicationFontSizeChoices(), nil)
	f.scrollback = entry(strconv.Itoa(Defaults().ScrollbackLines))

	f.pasteDelay = entry("0 for no pacing")
	f.consoleBaud = widget.NewSelect(consoleBaudChoices(), nil)
	f.pasteWarn = entry("0 never asks")

	f.logDir = entry(GetLogsDir())
	f.logStamped = widget.NewCheck("Prefix each line with a wall-clock time", nil)

	f.antiIdle = widget.NewCheck("Keep idle sessions alive", func(bool) {
		f.applyAntiIdleState()
	})
	f.antiIdleSecs = entry(strconv.Itoa(Defaults().AntiIdleIntervalSec))
	f.antiIdleKeys = widget.NewSelect(AntiIdleKeystrokeChoices, nil)

	f.status = statusLabel()

	f.tabs = container.NewAppTabs(
		container.NewTabItem("Appearance", f.appearanceTab()),
		container.NewTabItem("Terminal", f.terminalTab()),
		container.NewTabItem("Sessions", f.sessionsTab()),
		container.NewTabItem("Paths", f.pathsTab()),
	)

	f.content = container.NewBorder(nil, f.footer(), nil, nil, f.tabs)
}

func (f *SettingsForm) appearanceTab() fyne.CanvasObject {
	return container.NewVScroll(container.NewVBox(
		form(
			row("Application theme", f.appTheme),
			row("Terminal theme", f.termTheme),
			row("Terminal font size", f.fontSize),
		),
		widget.NewLabel("The chrome and the terminal palette are independent: the\n"+
			"shipped pairing is dark chrome around the light \"ice\" terminal.\n\n"+
			"The application theme changes immediately. A font size or palette\n"+
			"applies to tabs opened from now on — an open session keeps the\n"+
			"size it measured its grid at."),
	))
}

func (f *SettingsForm) terminalTab() fyne.CanvasObject {
	return container.NewVScroll(container.NewVBox(
		form(
			row("Scrollback lines", f.scrollback),
		),
		widget.NewSeparator(),
		widget.NewLabel("Paste pacing. The two delays solve different failures and\n"+
			"neither substitutes for the other: the line delay gives a device\n"+
			"time to parse the command it just received, the line speed gives a\n"+
			"console server time to clock out a long line."),
		form(
			row("Paste line delay (ms)", f.pasteDelay),
			row("Console line speed", f.consoleBaud),
			row("Warn at paste lines", f.pasteWarn),
		),
		widget.NewLabel("The paste warning fires whether or not the far end asked for\n"+
			"bracketed paste. Set it to 0 to never ask."),
	))
}

func (f *SettingsForm) sessionsTab() fyne.CanvasObject {
	f.antiIdleGroup = form(
		row("Interval (s)", f.antiIdleSecs),
		row("Keystroke", f.antiIdleKeys),
	)

	return container.NewVScroll(container.NewVBox(
		widget.NewLabel("Transcripts. A blank directory writes to the application's\n"+
			"logs directory, shown on the Paths page."),
		form(
			row("Transcript directory", f.logDir),
			row("Timestamps", f.logStamped),
		),
		widget.NewSeparator(),
		widget.NewLabel("Anti-idle sends a harmless keystroke after a quiet interval so a\n"+
			"session is not reaped by an exec-timeout while it is being read.\n"+
			"A session can override this."),
		form(row("Anti-idle", f.antiIdle)),
		f.antiIdleGroup,
	))
}

// pathsTab is read-only. Every row is a question somebody asks when a file is
// not where they expected, and the answer is one this run already knows.
func (f *SettingsForm) pathsTab() fyne.CanvasObject {
	details := f.resolvedPaths()

	// form, not formOf: formOf wraps its rows in a VScroll, which is right
	// for a tall launch dialog and wrong inside a VBox -- the scroll gets a
	// small height and every row but the first ends up below the fold with
	// a scrollbar beside it. The About box has the same bug.
	rows := make([][2]fyne.CanvasObject, 0, len(details))
	for _, d := range details {
		value := widget.NewLabel(d.Value)
		value.Wrapping = fyne.TextWrapBreak
		rows = append(rows, row(d.Label, value))
	}

	copyBtn := widget.NewButtonWithIcon("Copy paths", theme.ContentCopyIcon(), func() {
		lines := make([]string, 0, len(details))
		for _, d := range details {
			lines = append(lines, fmt.Sprintf("%s: %s", d.Label, d.Value))
		}
		// fyne.Window.Clipboard is deprecated in v2.6; the app-level one
		// is the supported route and is the same clipboard.
		fyne.CurrentApp().Clipboard().SetContent(strings.Join(lines, "\n") + "\n")
		f.status.SetText("paths copied")
	})

	return container.NewVScroll(container.NewVBox(
		widget.NewLabel("Where this run is reading and writing. Read-only —\n"+
			"these follow the command line and the settings above."),
		form(rows...),
		container.NewHBox(copyBtn, layout.NewSpacer()),
	))
}

// resolvedPaths is the application's own paths plus whatever the host added.
// The application's come first because they are the same on every run and the
// host's are the ones that move.
func (f *SettingsForm) resolvedPaths() []AboutDetail {
	logs := strings.TrimSpace(f.logDir.Text)
	if logs == "" {
		logs = GetLogsDir()
	} else {
		logs = ExpandHome(logs)
	}

	out := []AboutDetail{
		{Label: "Application home", Value: GetAppHome()},
		{Label: "Settings file", Value: SettingsPath()},
		{Label: "Transcripts", Value: logs},
		{Label: "Themes", Value: GetThemesDir()},
	}
	for _, d := range f.opts.Paths {
		if strings.TrimSpace(d.Value) == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (f *SettingsForm) footer() fyne.CanvasObject {
	bar := container.NewHBox(layout.NewSpacer())

	if f.opts.OnCancel != nil {
		bar.Add(widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), f.opts.OnCancel))
	}
	if f.opts.OnSave != nil {
		save := widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), func() {
			if s, ok := f.Settings(); ok {
				f.opts.OnSave(s)
			}
		})
		save.Importance = widget.HighImportance
		bar.Add(save)
	}

	return container.NewVBox(f.status, bar)
}

// applyAntiIdleState greys the interval and keystroke when anti-idle is off,
// rather than hiding them: the values are still what would be used if it were
// turned back on, and a row that disappears reads as a setting that was lost.
func (f *SettingsForm) applyAntiIdleState() {
	on := f.antiIdle.Checked
	setEnabled(f.antiIdleSecs, on)
	setEnabled(f.antiIdleKeys, on)
}

func (f *SettingsForm) themeLabelFor(key string) string {
	if l, ok := f.themeKeyToLabel[key]; ok {
		return l
	}
	return key
}

func (f *SettingsForm) themeKeyFor(label string) string {
	if k, ok := f.labelToThemeKey[label]; ok {
		return k
	}
	return label
}

// applicationFontSizeChoices is the supported range with no inherit entry.
func applicationFontSizeChoices() []string {
	out := make([]string, 0, MaxTerminalFontSize-MinTerminalFontSize+1)
	for s := MinTerminalFontSize; s <= MaxTerminalFontSize; s++ {
		out = append(out, strconv.Itoa(s))
	}
	return out
}

// ShowSettings opens the settings dialog over w. save is called with the
// accepted settings; the dialog closes only when the form was readable, so a
// refused value keeps the person in front of the field that caused it.
func ShowSettings(w fyne.Window, opts SettingsFormOptions) {
	var d dialog.Dialog

	inner := opts
	inner.OnCancel = func() {
		if opts.OnCancel != nil {
			opts.OnCancel()
		}
		d.Hide()
	}
	if opts.OnSave != nil {
		inner.OnSave = func(s Settings) {
			d.Hide()
			opts.OnSave(s)
		}
	}

	form := NewSettingsForm(inner)
	d = dialog.NewCustomWithoutButtons("Settings", form.Content(), w)
	d.Resize(fyne.NewSize(720, 620))
	d.Show()
}
