// internal/ui/sessionapply.go
//
// Turning a session node into the terminal's own settings.
//
// The session model knows nothing about this package and this file is where
// that gap is closed — deliberately in one place, because the alternative is
// every caller that opens a tab re-deriving "font size on the node means
// Settings.FontSize" and one of them getting it wrong in a way that only shows
// up as a grid two columns off.
//
// No toolkit import: this is settings arithmetic, so it stays in the part of
// the package that can be compiled and tested without a display.
package ui

import "github.com/scottpeterman/pathfinderssh/internal/sessions"

// SettingsFor returns base with the node's terminal overrides applied.
//
// Zero means "not overridden" for every numeric field. That is why the model
// normalizes before it gets here: a node that genuinely wants 12pt carries 12,
// not 0, so this cannot confuse "unset" with "the same as the default" in a
// way that would matter later.
//
// The application chrome is NOT touched. Chrome is light or dark for the whole
// window; deriving it per session would repaint the app when you changed tabs.
func SettingsFor(base Settings, n sessions.Node) Settings {
	n = n.Normalize()
	if n.TerminalTheme != "" && ThemeExists(n.TerminalTheme) {
		base.TerminalTheme = n.TerminalTheme
	}
	if n.FontSize > 0 {
		base.FontSize = ClampTerminalFontSize(n.FontSize)
	}
	if n.ScrollbackLines > 0 {
		base.ScrollbackLines = n.ScrollbackLines
	}
	// Negative is an explicit "no pacing on this session". It has to survive
	// as something other than zero, because zero is how a node says nothing
	// at all — and with a non-zero application default those two answers now
	// mean opposite things.
	if n.PasteLineDelayMs != 0 {
		base.PasteLineDelayMs = n.PasteLineDelayMs
		if base.PasteLineDelayMs < 0 {
			base.PasteLineDelayMs = 0
		}
	}
	if n.ConsoleBaud != 0 {
		// PasteBaud already resolves the negative "explicitly full speed"
		// case to zero, and the serial fallback. The guard is on the raw
		// field because zero there is the one value that means "the node
		// said nothing" and must leave the application setting alone.
		base.PasteConsoleBaud = n.PasteBaud()
	}
	// Same three-state rule as the line delay: 0 inherits, negative is an
	// explicit "never ask on this device".
	if n.PasteWarnLines != 0 {
		base.PasteWarnLines = n.PasteWarnLines
		if base.PasteWarnLines < 0 {
			base.PasteWarnLines = 0
		}
	}
	return base
}

// AntiIdleOverrideFor maps the node's three-state setting onto the override
// the anti-idle resolver takes. Inherit produces a nil-field override rather
// than no override, so the interval can still be pinned per device while the
// enabled state follows the application.
func AntiIdleOverrideFor(n sessions.Node) *AntiIdleOverride {
	over := &AntiIdleOverride{}
	if v, set := n.AntiIdle.Enabled(); set {
		over.Enabled = &v
	}
	if n.AntiIdle.IntervalSec > 0 {
		sec := n.AntiIdle.IntervalSec
		over.IntervalSec = &sec
	}
	return over
}

// ApplySession configures a terminal from its node: name, transcript, anti-idle
// and the per-session terminal palette.
//
// Call it before Attach. Anti-idle in particular is read when the transport is
// attached, so setting it afterwards silently does nothing until the next
// reconnect — the kind of miss that looks like the feature not working.
//
// Font size is NOT applied here and cannot be: widget.TextGrid renders at the
// application theme's text size, so a per-session size has to come from the
// theme override the session is wrapped in. The caller installs the settings
// (SettingsFor) and then wraps with Themed; doing it in the other order gives a
// grid whose arithmetic and whose glyphs disagree.
func ApplySession(s *Session, n sessions.Node) {
	if s == nil {
		return
	}
	n = n.Normalize()
	s.SetName(n.Label())
	// The one place every host configures a session from a node, so setting
	// the target label here gives all four front ends a paste confirmation
	// that names the device without any of them being touched.
	s.SetTargetLabel(pasteTargetLabel(n))
	s.SetSessionLogEnabled(n.LogEnabled)
	s.SetAntiIdle(ResolveAntiIdle(AntiIdleOverrideFor(n)))
	if n.TerminalTheme != "" {
		// An unregistered name reverts to inheriting the global setting
		// rather than failing the connect, which is what should happen
		// when a user deletes a palette a saved session still names.
		s.SetTerminalTheme(n.TerminalTheme)
	}
}
