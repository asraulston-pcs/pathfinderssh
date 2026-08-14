// internal/ui/sessionapply_test.go
//
// These cover the arithmetic, not the widgets: no toolkit is involved, so they
// run without a display and fail on a rule change rather than on a layout one.
package ui

import (
	"testing"

	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

func TestSettingsForLeavesTheAppChromeAlone(t *testing.T) {
	// The invariant the theme reduction exists to protect: a session may
	// choose its terminal palette and nothing else about the window. A
	// node that could reach AppTheme would repaint the app on tab change.
	base := Defaults()
	base.AppTheme = AppDark

	n := sessions.Defaults()
	n.TerminalTheme = DefaultTerminalTheme // a light palette

	got := SettingsFor(base, n)
	if got.AppVariant() != AppDark {
		t.Fatalf("app variant = %q, want it untouched at %q", got.AppVariant(), AppDark)
	}
	if got.TerminalThemeName() != DefaultTerminalTheme {
		t.Fatalf("terminal theme = %q", got.TerminalThemeName())
	}
}

func TestUnsetTerminalSettingsInheritTheApplication(t *testing.T) {
	base := Defaults()
	base.FontSize = 16
	base.ScrollbackLines = 5000
	base.TerminalTheme = DefaultTerminalTheme

	// A node that specifies nothing must not quietly pin the shipped
	// defaults; it follows whatever the application is set to.
	got := SettingsFor(base, sessions.Defaults())
	if got.FontSize != 16 {
		t.Errorf("font size = %d, want the application's 16", got.FontSize)
	}
	if got.ScrollbackLines != 5000 {
		t.Errorf("scrollback = %d, want the application's 5000", got.ScrollbackLines)
	}
}

func TestNodeOverridesWin(t *testing.T) {
	base := Defaults()
	base.FontSize = 16
	base.ScrollbackLines = 1000
	base.PasteLineDelayMs = 0

	n := sessions.Defaults()
	n.FontSize = 20
	n.ScrollbackLines = 200
	n.PasteLineDelayMs = 40

	got := SettingsFor(base, n)
	if got.FontSize != 20 || got.ScrollbackLines != 200 || got.PasteLineDelayMs != 40 {
		t.Fatalf("overrides not applied: %+v", got)
	}
}

func TestFontSizeIsClamped(t *testing.T) {
	base := Defaults()
	n := sessions.Defaults()

	n.FontSize = 400
	if got := SettingsFor(base, n).FontSize; got != MaxTerminalFontSize {
		t.Errorf("oversized font = %d, want clamp to %d", got, MaxTerminalFontSize)
	}
	n.FontSize = 2
	if got := SettingsFor(base, n).FontSize; got != MinTerminalFontSize {
		t.Errorf("undersized font = %d, want clamp to %d", got, MinTerminalFontSize)
	}
}

func TestUnknownPaletteFallsBackRatherThanFailing(t *testing.T) {
	base := Defaults()
	base.TerminalTheme = DefaultTerminalTheme

	n := sessions.Defaults()
	n.TerminalTheme = "a-palette-the-user-deleted"

	if got := SettingsFor(base, n).TerminalThemeName(); got != DefaultTerminalTheme {
		t.Fatalf("terminal theme = %q, want the base %q", got, DefaultTerminalTheme)
	}
}

func TestAntiIdleThreeStatesMapToTheOverride(t *testing.T) {
	cases := []struct {
		mode    sessions.AntiIdleMode
		wantSet bool
		wantVal bool
	}{
		{sessions.AntiIdleInherit, false, false},
		{sessions.AntiIdleOn, true, true},
		{sessions.AntiIdleOff, true, false},
	}
	for _, c := range cases {
		n := sessions.Defaults()
		n.AntiIdle.Mode = c.mode
		over := AntiIdleOverrideFor(n)
		if (over.Enabled != nil) != c.wantSet {
			t.Errorf("%q: Enabled set = %v, want %v", c.mode, over.Enabled != nil, c.wantSet)
			continue
		}
		if c.wantSet && *over.Enabled != c.wantVal {
			t.Errorf("%q: Enabled = %v, want %v", c.mode, *over.Enabled, c.wantVal)
		}
	}
}

func TestAntiIdleIntervalCanBePinnedWhileStateInherits(t *testing.T) {
	n := sessions.Defaults()
	n.AntiIdle = sessions.AntiIdleSpec{Mode: sessions.AntiIdleInherit, IntervalSec: 60}

	over := AntiIdleOverrideFor(n)
	if over.Enabled != nil {
		t.Error("inherit should leave the enabled state alone")
	}
	if over.IntervalSec == nil || *over.IntervalSec != 60 {
		t.Fatalf("interval override = %v, want 60", over.IntervalSec)
	}
}
