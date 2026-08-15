// internal/ui/settings.go
// The settings this package reads, and nothing else.
//
// TetherSSH had a 1,040-line settings.go: a JSON-backed AppSettings struct
// behind a global singleton, covering fonts, sessions, window geometry, themes
// and the vault. It is not being ported. This is the subset the terminal
// actually reads, as a plain value the application supplies once at startup.
//
// It is a struct rather than an interface because every field is a scalar the
// widget reads on a hot path, and because Defaults() then gives a working
// terminal with no settings layer wired up at all -- which is exactly the state
// the smoke harness runs in.
package ui

// AppVariant is the application chrome's appearance. There are two values and
// deliberately no "system": following the OS means the chrome can flip at
// sunset underneath a terminal palette the user chose and did not change, which
// is a combination nobody selected. A user who wants the chrome to follow the
// OS can change one setting.
type AppVariant string

const (
	AppDark  AppVariant = "dark"
	AppLight AppVariant = "light"
)

// DefaultAppVariant is the shipped chrome. Paired with DefaultTerminalTheme.
const DefaultAppVariant = AppDark

// Normalize maps anything unrecognised (including the empty string, which is
// what an unset settings field looks like) to the default.
func (v AppVariant) Normalize() AppVariant {
	if v == AppLight {
		return AppLight
	}
	return AppDark
}

// IsDark reports whether the chrome renders in Fyne's dark variant.
func (v AppVariant) IsDark() bool { return v.Normalize() == AppDark }

// Settings is the terminal's view of application settings.
type Settings struct {
	// AppTheme is the application chrome: light or dark, nothing else.
	// TerminalTheme names a palette from the theme registry. The two are
	// independent settings and neither derives from the other -- the shipped
	// default is dark chrome around the LIGHT "ice" terminal, so a rule that
	// derived one from the other would get the default exactly backwards.
	//
	// An empty AppTheme means unset, not light: SetSettings normalizes it to
	// the default, so a zero Settings still renders the shipped appearance.
	AppTheme      AppVariant `json:"app_theme"`
	TerminalTheme string     `json:"terminal_theme"`

	// FontSize is the terminal font size in points.
	FontSize int `json:"font_size"`

	// ScrollbackLines bounds the retained history.
	ScrollbackLines int `json:"scrollback_lines"`

	// RowOffset and ColOffset trim the computed grid size, for a display where
	// the area the toolkit reports and the area glyphs actually occupy still
	// disagree. Both default to zero and should stay there.
	//
	// TetherSSH shipped non-zero defaults and its users tuned these per
	// machine, but that was compensating for a bug rather than for any
	// property of a display: the renderer decided whether to resize using the
	// width minus the scrollbar gutter and then performed the resize with the
	// full width, so the grid was consistently two columns wider than it could
	// draw. With that fixed, a correct display needs no fudge. Treat a
	// non-zero value here as evidence of a measurement bug worth finding, not
	// as normal configuration.
	RowOffset int `json:"row_offset,omitempty"`
	ColOffset int `json:"col_offset,omitempty"`

	// PasteLineDelayMs paces multi-line paste, in milliseconds between lines.
	// Zero disables it. It is not cosmetic: a device with a slow line-editor
	// drops characters when a paste arrives faster than it can echo.
	PasteLineDelayMs int `json:"paste_line_delay_ms"`

	// PasteConsoleBaud paces paste WITHIN a line, at the speed of the async
	// line on the far side. Zero sends at full speed.
	//
	// The two settings solve different failures and neither substitutes for
	// the other: a line delay gives a device time to PARSE the command it just
	// received, while this gives a console server time to CLOCK OUT the
	// characters of a single line that is longer than its buffer.
	PasteConsoleBaud int `json:"paste_console_baud"`

	// PasteWarnLines is the line count at or above which a paste asks for
	// confirmation first. The default of 2 means any multi-line paste asks;
	// 0 turns the question off.
	//
	// The question fires REGARDLESS of bracketed paste. A carve-out there
	// was tried and reversed: a modern bash prompt requests DEC 2004, so
	// the guard never fired at the one prompt where pasting the wrong thing
	// is most likely. A safety feature with a clever exception is a safety
	// feature that is off when it matters.
	PasteWarnLines int `json:"paste_warn_lines"`

	// LogDirectory is where session transcripts are written. Empty means
	// GetLogsDir(). TimestampLogs prefixes each logged line with a wall-clock
	// time, which is what makes a transcript usable as evidence of when a
	// change actually landed.
	LogDirectory  string `json:"log_directory"`
	TimestampLogs bool   `json:"timestamp_logs"`

	// Anti-idle sends a harmless keystroke after a quiet interval, so a
	// session is not reaped by an exec-timeout while someone is reading.
	AntiIdleEnabled     bool   `json:"anti_idle_enabled"`
	AntiIdleIntervalSec int    `json:"anti_idle_interval_sec"`
	AntiIdleKeystroke   string `json:"anti_idle_keystroke"`

	// VaultPromptDeclined records that the first-run "no credential vault"
	// warning was answered with "Not now".
	//
	// It is state, not a preference: it has no row in the settings dialog,
	// and it lives here only because this is the file that already survives
	// a restart. Zero is the meaning (never asked, or asked and accepted),
	// so Normalized leaves it alone. Creating a vault clears it, since a
	// decline that no longer describes anything is just a stale flag.
	VaultPromptDeclined bool `json:"vault_prompt_declined,omitempty"`
}

// Defaults returns a usable configuration.
func Defaults() Settings {
	return Settings{
		AppTheme:        DefaultAppVariant,
		TerminalTheme:   DefaultTerminalTheme,
		FontSize:        12,
		ScrollbackLines: 1000,
		RowOffset:       0,
		ColOffset:       0,
		// 25ms is not free — it is a quarter second on a ten-line block —
		// and it is still cheaper than finding out later that one line of a
		// config went in half-parsed.
		PasteLineDelayMs:    25,
		PasteConsoleBaud:    0,
		PasteWarnLines:      2,
		AntiIdleEnabled:     false,
		AntiIdleIntervalSec: antiIdleDefaultIntervalSec,
		AntiIdleKeystroke:   antiIdleDefaultKeystroke,
	}
}

// current is read on render paths and written once at startup.
var current = Defaults()

// Normalized fills in every unset field and resolves the values that other
// code would otherwise have to defend against.
//
// It exists as a method rather than as the body of SetSettings because there
// are now two ways settings arrive -- installed at startup, and read off disk
// by LoadSettings -- and a rule applied in only one of them is a rule that
// holds until somebody edits the file by hand.
//
// Zero is "unset" for the fields with a non-zero default, so it fills. Zero is
// a MEANING for the paste fields (no pacing, never ask) so it is left alone,
// and a negative there is folded to zero: the three-state sentinels belong to
// sessions.Node, which SettingsFor has already resolved by the time a value
// reaches here.
func (s Settings) Normalized() Settings {
	s.AppTheme = s.AppTheme.Normalize()
	if s.TerminalTheme == "" {
		s.TerminalTheme = DefaultTerminalTheme
	}
	s.FontSize = ClampTerminalFontSize(s.FontSize)
	if s.ScrollbackLines <= 0 {
		s.ScrollbackLines = 1000
	}
	if s.PasteLineDelayMs < 0 {
		s.PasteLineDelayMs = 0
	}
	if s.PasteConsoleBaud < 0 {
		s.PasteConsoleBaud = 0
	}
	if s.PasteWarnLines < 0 {
		s.PasteWarnLines = 0
	}
	// The transcript directory is typed into a text field, which is the one
	// place a leading ~ is never expanded for us.
	s.LogDirectory = ExpandHome(s.LogDirectory)
	if s.AntiIdleIntervalSec <= 0 {
		s.AntiIdleIntervalSec = antiIdleDefaultIntervalSec
	}
	if s.AntiIdleKeystroke == "" {
		s.AntiIdleKeystroke = antiIdleDefaultKeystroke
	}
	return s
}

// SetSettings installs the application's settings. Call it before the first
// theme lookup or terminal construction; it is not safe to call concurrently
// with a running terminal.
func SetSettings(s Settings) {
	current = s.Normalized()
}

// CurrentSettings returns the installed settings.
func CurrentSettings() Settings { return current }

// AppVariant is the resolved application chrome variant.
func (s Settings) AppVariant() AppVariant { return s.AppTheme.Normalize() }

// TerminalThemeName is the resolved terminal theme name.
func (s Settings) TerminalThemeName() string {
	if s.TerminalTheme == "" {
		return DefaultTerminalTheme
	}
	return s.TerminalTheme
}
