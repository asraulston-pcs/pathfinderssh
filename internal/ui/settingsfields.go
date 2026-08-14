// internal/ui/settingsfields.go
// The settings dialog's model: strings in, Settings out, and the reasons a
// value was refused.
//
// Everything here is what the dialog would otherwise do inline, lifted out so
// it can be tested without a display -- the same split as aboutinfo.go under
// about.go. The dialog builds widgets, reads their text into SettingsFields,
// and calls Apply. It makes no decisions of its own.
//
// # Why Apply takes a base
//
// A form does not carry every setting. RowOffset and ColOffset are deliberately
// not on it (a non-zero value there is evidence of a measurement bug, not
// configuration -- see settings.go), and anything added to Settings later will
// not be on it either. Applying onto a base preserves those instead of
// resetting them to the zero value every time somebody changes the font size.
//
// # Why a refusal rather than a clamp
//
// A form that silently rewrites what was typed teaches people that it did not
// read them. Out-of-range values are reported with the range, and the dialog
// does not close. Blank is the one exception: it means "leave this alone",
// because an empty field is what a cleared entry looks like and clearing an
// entry is not the same as asking for zero.
package ui

import (
	"fmt"
	"strconv"
	"strings"
)

// Bounds the form enforces. The paste and interval ceilings are not physical
// limits -- they are the point past which a value is more likely a typo than
// an intention. A 5-second-per-line paste is 8 minutes on a 100-line config.
const (
	MinScrollbackLines = 100
	MaxScrollbackLines = 1000000

	MaxPasteLineDelayMs = 5000
	MaxPasteWarnLines   = 1000

	MinAntiIdleIntervalSec = 10
	MaxAntiIdleIntervalSec = 86400
)

// SettingsFieldError names the field a value was refused for, so the dialog can
// say which row is wrong instead of reporting that something, somewhere, is.
type SettingsFieldError struct {
	Field   string
	Problem string
}

func (e SettingsFieldError) Error() string { return e.Field + ": " + e.Problem }

// SettingsFields is the settings dialog as data. Text fields and dropdowns are
// strings because that is what the widgets hold; checkboxes are bools for the
// same reason.
//
// TerminalTheme is a theme KEY, not the label shown in the dropdown. The dialog
// translates through ThemeMenuData, which is the only place that mapping
// exists.
type SettingsFields struct {
	AppTheme      string
	TerminalTheme string
	FontSize      string

	ScrollbackLines  string
	PasteLineDelayMs string
	PasteConsoleBaud string
	PasteWarnLines   string

	LogDirectory  string
	TimestampLogs bool

	AntiIdleEnabled     bool
	AntiIdleIntervalSec string
	AntiIdleKeystroke   string
}

// SettingsFieldsOf renders settings into form values.
func SettingsFieldsOf(s Settings) SettingsFields {
	s = s.Normalized()
	return SettingsFields{
		AppTheme:      string(s.AppVariant()),
		TerminalTheme: s.TerminalThemeName(),
		FontSize:      strconv.Itoa(s.FontSize),

		ScrollbackLines:  strconv.Itoa(s.ScrollbackLines),
		PasteLineDelayMs: strconv.Itoa(s.PasteLineDelayMs),
		PasteConsoleBaud: consoleBaudLabel(s.PasteConsoleBaud),
		PasteWarnLines:   strconv.Itoa(s.PasteWarnLines),

		LogDirectory:  s.LogDirectory,
		TimestampLogs: s.TimestampLogs,

		AntiIdleEnabled:     s.AntiIdleEnabled,
		AntiIdleIntervalSec: strconv.Itoa(s.AntiIdleIntervalSec),
		AntiIdleKeystroke:   s.AntiIdleKeystroke,
	}
}

// Apply folds the form values onto base and returns the result with every
// field that was refused.
//
// On any error the returned Settings is still the best reading of the form --
// the good fields applied, the bad ones left at their base value -- so a caller
// that wants to show a preview alongside the errors can.
func (f SettingsFields) Apply(base Settings) (Settings, []SettingsFieldError) {
	var errs []SettingsFieldError
	out := base

	if v := strings.TrimSpace(f.AppTheme); v != "" {
		out.AppTheme = AppVariant(strings.ToLower(v)).Normalize()
	}

	if v := strings.TrimSpace(f.TerminalTheme); v != "" {
		if !ThemeExists(v) {
			errs = append(errs, SettingsFieldError{"Terminal theme",
				fmt.Sprintf("no theme named %q is loaded", v)})
		} else {
			out.TerminalTheme = v
		}
	}

	if n, err := settingsInt("Font size", f.FontSize,
		MinTerminalFontSize, MaxTerminalFontSize); err != nil {
		errs = appendErr(errs, err)
	} else if n != nil {
		out.FontSize = *n
	}

	if n, err := settingsInt("Scrollback lines", f.ScrollbackLines,
		MinScrollbackLines, MaxScrollbackLines); err != nil {
		errs = appendErr(errs, err)
	} else if n != nil {
		out.ScrollbackLines = *n
	}

	if n, err := settingsInt("Paste line delay", f.PasteLineDelayMs,
		0, MaxPasteLineDelayMs); err != nil {
		errs = appendErr(errs, err)
	} else if n != nil {
		out.PasteLineDelayMs = *n
	}

	if n, err := settingsInt("Warn at paste lines", f.PasteWarnLines,
		0, MaxPasteWarnLines); err != nil {
		errs = appendErr(errs, err)
	} else if n != nil {
		out.PasteWarnLines = *n
	}

	// The console speed comes from a dropdown whose "off" entry is a phrase
	// rather than a number, so it is read before anything tries to parse it.
	switch v := strings.TrimSpace(f.PasteConsoleBaud); {
	case v == "":
	case v == ConsoleBaudFull:
		out.PasteConsoleBaud = 0
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(errs, SettingsFieldError{"Console line speed",
				fmt.Sprintf("%q is not a line speed", v)})
		} else {
			out.PasteConsoleBaud = n
		}
	}

	// A transcript directory is a path typed into a text field, which is the
	// one place a leading ~ is never expanded for us. Blank is legitimate and
	// means the default logs directory, so it is not an error.
	out.LogDirectory = ExpandHome(f.LogDirectory)
	out.TimestampLogs = f.TimestampLogs

	out.AntiIdleEnabled = f.AntiIdleEnabled
	if n, err := settingsInt("Anti-idle interval", f.AntiIdleIntervalSec,
		MinAntiIdleIntervalSec, MaxAntiIdleIntervalSec); err != nil {
		errs = appendErr(errs, err)
	} else if n != nil {
		out.AntiIdleIntervalSec = *n
	}

	if v := strings.TrimSpace(f.AntiIdleKeystroke); v != "" {
		if !isAntiIdleKeystroke(v) {
			errs = append(errs, SettingsFieldError{"Anti-idle keystroke",
				fmt.Sprintf("%q is not one of the supported keystrokes", v)})
		} else {
			out.AntiIdleKeystroke = v
		}
	}

	return out.Normalized(), errs
}

// settingsInt parses one bounded integer field. A nil result with a nil error
// is a blank field, which means "leave it alone" rather than zero.
func settingsInt(label, text string, min, max int) (*int, *SettingsFieldError) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return nil, &SettingsFieldError{label, fmt.Sprintf("%q is not a number", text)}
	}
	if n < min || n > max {
		return nil, &SettingsFieldError{label,
			fmt.Sprintf("must be between %d and %d", min, max)}
	}
	return &n, nil
}

func appendErr(errs []SettingsFieldError, e *SettingsFieldError) []SettingsFieldError {
	if e == nil {
		return errs
	}
	return append(errs, *e)
}

func isAntiIdleKeystroke(name string) bool {
	for _, c := range AntiIdleKeystrokeChoices {
		if c == name {
			return true
		}
	}
	return false
}

// SettingsProblems renders refusals as one message for a status line.
func SettingsProblems(errs []SettingsFieldError) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}
