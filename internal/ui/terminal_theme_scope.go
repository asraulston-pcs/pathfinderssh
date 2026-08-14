// internal/ui/terminal_theme_scope.go
// cli/terminal_theme_scope.go - Per-terminal (per-session) terminal theme.
//
// The terminal palette used to be resolved through package-level functions
// (GetTerminalColorMappings, GetTerminalForeground, IsDarkTerminal,
// Xterm256Color) that read the single global setting, so every open tab was
// forced to the same terminal theme. This file scopes that resolution to one
// widget, so a session can carry its own terminal theme - a red-tinted CRT for
// a core router, a light theme for a build host - while tabs without an
// override keep following Settings -> Appearance -> Terminal Theme.
//
// The scope also removes a per-cell cost. terminalColorMap() builds a fresh
// 18-entry map on every call, and mapColor called it once per changed cell per
// frame. Here the map is built once per (widget, theme) and rebuilt only when
// the resolved theme name actually changes, so live theme switching still works
// while a typing frame costs no map allocations at all.
// Threading: the scope is render-path state, mutated only from the Fyne main
// thread (SetTerminalTheme at connect, refresh from the redraw path), so it is
// deliberately lock-free - the redraw loop reads it thousands of times a frame.
package ui

import (
	"image/color"
)

// termThemeScope is one terminal widget's resolved palette.
//
// override is the session's choice and is sticky; resolved is the theme name the
// cached palette was actually built from, which for an inheriting terminal
// follows the global setting and changes underneath us when the user switches
// themes. Comparing the two on every access is what makes the cache safe.
type termThemeScope struct {
	override string // session-level theme name; "" = inherit the global setting
	resolved string // theme name the cache below was built from
	palette  map[string]color.Color
	bg       color.Color
	fg       color.Color
	dark     bool
}

// SetTerminalTheme binds this terminal to a named terminal theme. An empty name
// (or one that isn't registered - a YAML theme the user has since deleted)
// reverts to inheriting the global setting rather than failing the connect.
// Safe to call before or after the widget is rendered; the base fill repaints
// in place when it already exists.
func (t *NativeTerminalWidget) SetTerminalTheme(name string) {
	if name != "" && !ThemeExists(name) {
		name = ""
	}
	t.termTheme.override = name
	t.termTheme.palette = nil // force a rebuild on next access
	t.refreshTermTheme()

	if t.baseBG != nil {
		t.baseBG.FillColor = t.termBG()
		t.baseBG.Refresh()
	}
	t.updatePending.Store(true)
}

// TerminalThemeName returns the theme this terminal is bound to, or "" when it
// inherits the global setting.
func (t *NativeTerminalWidget) TerminalThemeName() string {
	return t.termTheme.override
}

// refreshTermTheme rebuilds the cached palette when the resolved theme has
// changed (or nothing is cached yet). Cheap enough to call on every lookup: the
// steady-state path is one string compare.
func (t *NativeTerminalWidget) refreshTermTheme() {
	want := t.termTheme.override
	if want == "" {
		want = globalTerminalThemeName()
	}
	if t.termTheme.palette != nil && t.termTheme.resolved == want {
		return
	}
	def := GetThemeDef(want)
	t.termTheme.resolved = want
	t.termTheme.palette = def.terminalColorMap()
	t.termTheme.dark = def.IsDark()
	t.termTheme.bg = hexOr(def.Terminal.Background, color.RGBA{0x00, 0x05, 0x10, 0xff})
	if fg := t.termTheme.palette["default"]; fg != nil {
		t.termTheme.fg = fg
	} else {
		t.termTheme.fg = color.RGBA{0xe0, 0xe0, 0xe0, 0xff}
	}
}

// globalTerminalThemeName is the app-wide terminal theme an un-overridden
// terminal follows.
func globalTerminalThemeName() string {
	return CurrentSettings().TerminalThemeName()
}

// termPalette is this terminal's ANSI palette (the 16 named slots plus
// "default"). Replaces GetTerminalColorMappings() at every render call site.
func (t *NativeTerminalWidget) termPalette() map[string]color.Color {
	t.refreshTermTheme()
	return t.termTheme.palette
}

// termBG is this terminal's pane background.
func (t *NativeTerminalWidget) termBG() color.Color {
	t.refreshTermTheme()
	return t.termTheme.bg
}

// termFG is this terminal's default (unset-SGR) text color. Never nil - Fyne
// renders an unset foreground as a near-black that vanishes on a dark pane.
func (t *NativeTerminalWidget) termFG() color.Color {
	t.refreshTermTheme()
	return t.termTheme.fg
}

// termIsDark reports whether this terminal's pane is dark, which is what decides
// the bold-black promotion (htop's primary text).
func (t *NativeTerminalWidget) termIsDark() bool {
	t.refreshTermTheme()
	return t.termTheme.dark
}

// termXterm256 is Xterm256Color scoped to this terminal: indices 0-15 resolve
// through this widget's palette, 16-255 are absolute (cube + grayscale) and
// identical for every theme, so they defer to the package-level function and
// stay safe to memoize globally.
func (t *NativeTerminalWidget) termXterm256(n int) color.Color {
	if n >= 0 && n < 16 {
		if c, ok := t.termPalette()[xterm256BaseNames[n]]; ok {
			return c
		}
		return nil
	}
	return Xterm256Color(n)
}
