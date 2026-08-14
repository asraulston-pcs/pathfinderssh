// internal/ui/theme_test.go
// The invariants of the two-settings appearance model.
//
// Everything here is data, so none of it needs a display or a running app. The
// one thing these tests exist to catch is the reduction being undone by
// something plausible: a terminal palette derived from the application variant,
// or a chrome block reappearing in a shipped theme file where yaml.v3 would
// ignore it in silence.
package ui

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDefaultPairsDarkChromeWithALightTerminal is the reason the two settings
// are independent rather than one deriving from the other. ice.yaml is
// type: "light" with a near-white background, and it is the shipped terminal
// default under DARK chrome. Any rule that derived terminal contrast from the
// app variant would ship the exact opposite of this pairing, so this test fails
// the moment such a rule is introduced.
func TestDefaultPairsDarkChromeWithALightTerminal(t *testing.T) {
	if !DefaultAppVariant.IsDark() {
		t.Errorf("default app variant = %q, want dark", DefaultAppVariant)
	}

	def := GetThemeDef(DefaultTerminalTheme)
	if def.Name != DefaultTerminalTheme {
		t.Fatalf("default terminal theme %q is not registered (got %q) -- the embedded pack did not load",
			DefaultTerminalTheme, def.Name)
	}
	if def.IsDark() {
		t.Errorf("default terminal theme %q reports dark; the whole point of the default pairing is a light terminal under dark chrome",
			DefaultTerminalTheme)
	}

	d := Defaults()
	if d.AppVariant() != DefaultAppVariant || d.TerminalThemeName() != DefaultTerminalTheme {
		t.Errorf("Defaults() = (%q, %q), want (%q, %q)",
			d.AppVariant(), d.TerminalThemeName(), DefaultAppVariant, DefaultTerminalTheme)
	}
}

// TestTerminalDarknessComesFromThePaletteNotTheChrome pins the direction of the
// dependency: setting the chrome light or dark must not move any terminal's
// reported darkness.
func TestTerminalDarknessComesFromThePaletteNotTheChrome(t *testing.T) {
	restore := CurrentSettings()
	t.Cleanup(func() { SetSettings(restore) })

	for _, name := range []string{DefaultTerminalTheme, ThemeCyber, ThemeLight, ThemeCorporate} {
		want := GetThemeDef(name).IsDark()
		for _, variant := range []AppVariant{AppDark, AppLight} {
			s := Defaults()
			s.AppTheme = variant
			s.TerminalTheme = name
			SetSettings(s)

			if got := GetThemeDef(CurrentSettings().TerminalThemeName()).IsDark(); got != want {
				t.Errorf("terminal %q under %q chrome: IsDark = %v, want %v",
					name, variant, got, want)
			}
		}
	}
}

func TestAppVariantNormalizes(t *testing.T) {
	tests := []struct {
		in   AppVariant
		want AppVariant
	}{
		{"", DefaultAppVariant}, // unset field, not a choice of light
		{"dark", AppDark},
		{"light", AppLight},
		{"cyber", DefaultAppVariant}, // a terminal palette name is not a chrome variant
		{"Dark", DefaultAppVariant},  // unrecognised spelling falls back rather than failing
	}
	for _, tc := range tests {
		if got := tc.in.Normalize(); got != tc.want {
			t.Errorf("AppVariant(%q).Normalize() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSetSettingsKeepsAnExplicitLightChrome guards the trap in representing the
// variant: the default is dark, so a naive zero-value check turns an explicit
// light choice back into dark.
func TestSetSettingsKeepsAnExplicitLightChrome(t *testing.T) {
	restore := CurrentSettings()
	t.Cleanup(func() { SetSettings(restore) })

	s := Defaults()
	s.AppTheme = AppLight
	SetSettings(s)
	if got := CurrentSettings().AppVariant(); got != AppLight {
		t.Errorf("explicit light chrome came back as %q", got)
	}

	SetSettings(Settings{}) // everything unset
	if got := CurrentSettings().AppVariant(); got != DefaultAppVariant {
		t.Errorf("unset chrome = %q, want %q", got, DefaultAppVariant)
	}
	if got := CurrentSettings().TerminalThemeName(); got != DefaultTerminalTheme {
		t.Errorf("unset terminal theme = %q, want %q", got, DefaultTerminalTheme)
	}
}

// TestNoShippedThemeCarriesAChromeBlock is the guard that makes the strip
// permanent. A chrome block in a theme file is not an error to yaml.v3 -- it is
// silently dropped -- so someone editing one would change colors, see nothing
// happen, and get no message saying why. The failure has to come from here.
func TestNoShippedThemeCarriesAChromeBlock(t *testing.T) {
	entries, err := bundledThemes.ReadDir("themes")
	if err != nil {
		t.Fatalf("embedded theme pack unreadable: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded theme pack is empty")
	}

	for _, e := range entries {
		data, err := bundledThemes.ReadFile("themes/" + e.Name())
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		var raw map[string]any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			t.Errorf("%s: parse error: %v", e.Name(), err)
			continue
		}
		if _, ok := raw["chrome"]; ok {
			t.Errorf("%s still has a chrome: block; the application chrome is light/dark and does not read theme files, so those keys do nothing", e.Name())
		}
	}
}

// TestEveryShippedThemeRegistersWithAUsablePalette catches a theme file that
// parses but yields nothing to render with -- a missing terminal block, or a
// name collision that silently replaced another palette.
func TestEveryShippedThemeRegistersWithAUsablePalette(t *testing.T) {
	entries, err := bundledThemes.ReadDir("themes")
	if err != nil {
		t.Fatalf("embedded theme pack unreadable: %v", err)
	}

	for _, e := range entries {
		data, err := bundledThemes.ReadFile("themes/" + e.Name())
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		def := parseThemeYAML(e.Name(), data)
		if def == nil {
			t.Errorf("%s: did not parse into a theme", e.Name())
			continue
		}
		if !ThemeExists(def.Name) {
			t.Errorf("%s: theme %q did not register", e.Name(), def.Name)
			continue
		}
		if def.Terminal.Background == "" || def.Terminal.Foreground == "" {
			t.Errorf("%s: theme %q has no terminal background/foreground", e.Name(), def.Name)
		}
		palette := def.terminalColorMap()
		for _, slot := range xterm256BaseNames {
			if palette[slot] == nil {
				t.Errorf("%s: theme %q has no color for %q", e.Name(), def.Name, slot)
			}
		}
		if palette["default"] == nil {
			t.Errorf("%s: theme %q has no default color", e.Name(), def.Name)
		}
	}
}

// TestUnknownTerminalThemeFallsBackRatherThanFailing covers the session-file
// case: a node carrying terminal_theme: <a palette this install does not have>
// must render on the fallback, never refuse to open.
func TestUnknownTerminalThemeFallsBackRatherThanFailing(t *testing.T) {
	def := GetThemeDef("a-palette-nobody-shipped")
	if def == nil {
		t.Fatal("GetThemeDef returned nil; every caller dereferences it")
	}
	if def.Name != ThemeCyber {
		t.Errorf("unknown theme fell back to %q, want %q", def.Name, ThemeCyber)
	}
	if ThemeExists("a-palette-nobody-shipped") {
		t.Error("ThemeExists reported an unregistered theme")
	}
}

// TestXterm256LeavesTheLowSixteenToThePalette pins the split that lets 16-255 be
// memoized globally: those indices are identical under every palette, the low 16
// are not, so the package-level function must refuse them rather than answer
// from a global setting.
func TestXterm256LeavesTheLowSixteenToThePalette(t *testing.T) {
	for n := 0; n < 16; n++ {
		if c := Xterm256Color(n); c != nil {
			t.Errorf("Xterm256Color(%d) = %v, want nil (palette-dependent, resolved per widget)", n, c)
		}
	}
	if Xterm256Color(16) == nil {
		t.Error("Xterm256Color(16) is nil; the color cube is absolute and must resolve")
	}
	if Xterm256Color(255) == nil {
		t.Error("Xterm256Color(255) is nil; the grayscale ramp is absolute and must resolve")
	}
	if Xterm256Color(256) != nil || Xterm256Color(-1) != nil {
		t.Error("out-of-range index resolved to a color")
	}
}
