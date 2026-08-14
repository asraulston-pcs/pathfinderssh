// internal/ui/theme.go
// The two independent appearance settings: the application chrome variant
// (light or dark, nothing else) and the terminal palette.
//
// They are independent on purpose and neither derives from the other. The
// shipped default is a DARK application chrome with the "ice" terminal palette,
// and ice.yaml is `type: "light"` -- a near-white terminal background under dark
// chrome. Anything that derived terminal contrast from the app variant would
// ship the exact opposite of the intended default, so nothing does: a terminal's
// darkness comes from its own palette's `type` field (see termIsDark in
// terminal_theme_scope.go) and the chrome's comes from AppVariant.
//
// The chrome used to be a full 13-slot palette loaded from the same YAML as the
// terminal, with per-theme override buckets in settings. That collapsed to
// light/dark: the chrome is Fyne's own theme at a forced variant, with a
// density adjustment. The terminal palette library was kept -- it earned its
// keep -- and is resolved per widget rather than globally.
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
)

// Themed wraps a terminal for display at the configured terminal font size.
//
// This is not optional decoration. widget.TextGrid has no per-grid font size --
// it always renders at theme.SizeNameText -- so a terminal placed straight into
// a window renders at the application theme's size while every calculation in
// the widget (grid rows and columns, hit testing, the selection overlay) uses
// Settings.FontSize. The two then disagree, and changing the font size appears
// to do nothing except reflow the remote to the wrong dimensions.
//
// TetherSSH did this inline at each of its three tab-creation sites. It is a
// function here so there is one place to get it right.
//
// The base theme is the app chrome, not the terminal palette: the terminal's
// colors come from its own per-widget scope, and what this override changes is
// only the text size.
func Themed(obj fyne.CanvasObject) fyne.CanvasObject {
	return ThemedAt(obj, CurrentSettings())
}

// ThemedAt is Themed against an explicit settings snapshot rather than the
// current global one.
//
// This is the form a tabbed shell needs. Settings are process-wide, so with one
// session on screen "install the session's settings, then wrap" is enough --
// which is what pfterm and pfconnect do. With several sessions alive at once
// there is no single current value to read: the last tab opened would decide
// the font size for every tab, including ones that were wrapped before it
// existed.
//
// The override object this returns holds the size, so it keeps rendering at
// that size regardless of what the global settings do afterwards. That is the
// property that makes a per-session font size survive opening a second tab, and
// it is why the shell restores the base settings immediately after mounting.
func ThemedAt(obj fyne.CanvasObject, cfg Settings) fyne.CanvasObject {
	size := float32(ClampTerminalFontSize(cfg.FontSize))
	return container.NewThemeOverride(obj, NewTerminalFontTheme(NewNativeTheme(cfg.AppVariant()), size))
}

// --- application chrome ---------------------------------------------------
//
// AppVariant and its default live in settings.go: they are settings data with
// no toolkit in them, which is what lets the appearance invariants be tested
// without a display.

// NativeTheme is the application chrome: Fyne's own theme pinned to one variant,
// with a density adjustment. It carries no palette of its own -- the 13-slot
// chrome palette and its per-theme override buckets were removed when the
// chrome collapsed to light/dark.
type NativeTheme struct {
	fyne.Theme
	dark bool
}

// NewNativeTheme builds the chrome theme for a variant.
func NewNativeTheme(v AppVariant) *NativeTheme {
	return &NativeTheme{Theme: theme.DefaultTheme(), dark: v.IsDark()}
}

// ApplyAppTheme installs the chrome on a Fyne app. Without this the app renders
// at whatever variant the OS reports, which is the "system" behaviour the two-
// value setting exists to avoid. Call it once, immediately after app.New().
func ApplyAppTheme(a fyne.App, v AppVariant) {
	a.Settings().SetTheme(NewNativeTheme(v))
}

// Color pins the variant. Fyne passes the variant it inferred from the OS; the
// whole point of a two-value setting is that the user's choice wins, so the
// argument is replaced rather than consulted.
func (t *NativeTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	variant := theme.VariantLight
	if t.dark {
		variant = theme.VariantDark
	}
	return t.Theme.Color(name, variant)
}

func (t *NativeTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (t *NativeTheme) Size(name fyne.ThemeSizeName) float32 {
	// App-wide density: a slightly smaller base text size and tighter padding
	// make the session tree (and dialogs/menus) more compact without looking
	// cramped. Everything else falls through to the default theme.
	switch name {
	case theme.SizeNameText:
		return 12 // default 14
	case theme.SizeNameInnerPadding:
		return 6 // default 8; trims the inset around label/entry/button content
	case theme.SizeNamePadding:
		return 3 // default 4; trims spacing between widgets in layouts
	}
	return theme.DefaultTheme().Size(name)
}

func (t *NativeTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// --- terminal font size ------------------------------------------------------

// Terminal font size bounds. The floor keeps text legible (accessibility); the
// ceiling keeps a single cell from dwarfing the viewport. Used by both the
// settings validation and terminal construction so they cannot disagree.
const (
	MinTerminalFontSize = 8
	MaxTerminalFontSize = 28
)

// ClampTerminalFontSize constrains a requested font size to the supported
// range, falling back to the default when the value is unset/zero.
func ClampTerminalFontSize(size int) int {
	if size <= 0 {
		return 12
	}
	if size < MinTerminalFontSize {
		return MinTerminalFontSize
	}
	if size > MaxTerminalFontSize {
		return MaxTerminalFontSize
	}
	return size
}

// terminalFontTheme wraps a base theme and overrides ONLY the base text size.
// A widget.TextGrid has no per-grid font size; it always renders at
// theme.SizeNameText. Wrapping the terminal in container.NewThemeOverride with
// this theme therefore lets the terminal render at its own font size while the
// application chrome keeps the app theme's size. Every other property delegates
// to base, so the override is a complete theme (ThemeOverride replaces, not
// inherits). Because the terminal's pixel<->cell mapping is measured from the
// rendered grid (see NativeTerminalWidget.gridCellSize), hit-testing, the
// selection overlay, and the PTY row/column count all follow this size
// automatically.
type terminalFontTheme struct {
	base     fyne.Theme
	fontSize float32
}

// NewTerminalFontTheme builds a terminal-scoped theme over base that reports
// fontSize for SizeNameText.
func NewTerminalFontTheme(base fyne.Theme, fontSize float32) fyne.Theme {
	return &terminalFontTheme{base: base, fontSize: fontSize}
}

func (t *terminalFontTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return t.base.Color(name, variant)
}

func (t *terminalFontTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

func (t *terminalFontTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

func (t *terminalFontTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == theme.SizeNameText {
		return t.fontSize
	}
	return t.base.Size(name)
}
