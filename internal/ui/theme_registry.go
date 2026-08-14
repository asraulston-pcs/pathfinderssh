// internal/ui/theme_registry.go
// theme_registry.go - data-driven registry of TERMINAL palettes.
//
// A theme is pure data: the ANSI 16 plus background/foreground/cursor/selection
// for the terminal grid. Built-in themes are registered at init; the theme pack
// is embedded from internal/ui/themes/*.yaml; user themes are loaded from
// ~/.pathfinderssh/themes/*.yaml at startup and layer on top (a user file whose
// `name` matches a built-in replaces it; a new name adds a theme). Precedence,
// lowest to highest: hardcoded built-ins, embedded pack, user files on disk.
//
// Adding a theme = dropping one YAML file in that directory.
//
// A theme no longer carries an application-chrome palette. The chrome is
// light/dark and comes from AppVariant (see theme.go), independent of whichever
// terminal palette is selected -- the shipped default pairs dark chrome with
// the light "ice" terminal, so the two genuinely disagree and neither can be
// derived from the other. A `chrome:` block in a theme file is ignored;
// yaml.v3 drops unknown keys silently, so the shipped files had theirs removed
// rather than left to be edited with no effect.

package ui

import (
	"embed"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ThemeTerminal is the terminal pane palette: background/foreground/cursor/
// selection plus the 16 ANSI slots. cursor and selection are part of the data
// model for completeness; today the renderer consumes background, foreground,
// and the 16 ANSI colors.
type ThemeTerminal struct {
	Background    string `yaml:"background"`
	Foreground    string `yaml:"foreground"`
	Cursor        string `yaml:"cursor"`
	Selection     string `yaml:"selection"`
	Black         string `yaml:"black"`
	Red           string `yaml:"red"`
	Green         string `yaml:"green"`
	Yellow        string `yaml:"yellow"`
	Blue          string `yaml:"blue"`
	Magenta       string `yaml:"magenta"`
	Cyan          string `yaml:"cyan"`
	White         string `yaml:"white"`
	BrightBlack   string `yaml:"bright_black"`
	BrightRed     string `yaml:"bright_red"`
	BrightGreen   string `yaml:"bright_green"`
	BrightYellow  string `yaml:"bright_yellow"`
	BrightBlue    string `yaml:"bright_blue"`
	BrightMagenta string `yaml:"bright_magenta"`
	BrightCyan    string `yaml:"bright_cyan"`
	BrightWhite   string `yaml:"bright_white"`
}

// ThemeDef is one complete terminal palette. Type is "dark" or "light" and is
// the ONLY thing that decides whether this terminal counts as dark -- never the
// application variant, which is a separate setting and disagrees with the
// terminal in the shipped default.
type ThemeDef struct {
	Name     string        `yaml:"name"`
	Label    string        `yaml:"label"`
	Type     string        `yaml:"type"`
	Terminal ThemeTerminal `yaml:"terminal"`
}

// IsDark reports whether the terminal pane renders dark. Used to promote
// otherwise-invisible colors (e.g. bold black) to a legible value.
func (t *ThemeDef) IsDark() bool { return t == nil || !strings.EqualFold(t.Type, "light") }

var (
	themeRegistry = map[string]*ThemeDef{}
	themeOrder    []string // insertion order: built-ins first, then user themes
)

// registerTheme adds or replaces a theme by name, preserving first-seen order.
func registerTheme(t *ThemeDef) {
	if t == nil || t.Name == "" {
		return
	}
	if _, exists := themeRegistry[t.Name]; !exists {
		themeOrder = append(themeOrder, t.Name)
	}
	themeRegistry[t.Name] = t
}

// GetThemeDef returns the theme by name, falling back to the cyber built-in so
// callers never get nil (e.g. a settings file naming a since-deleted theme, or
// a session node carrying a terminal_theme that this install does not have).
// Falling back rather than failing is deliberate: an unknown palette name must
// never be the reason a session will not connect.
func GetThemeDef(name string) *ThemeDef {
	if t, ok := themeRegistry[name]; ok {
		return t
	}
	return themeRegistry[ThemeCyber]
}

// ThemeExists reports whether a theme name is registered (used for validation).
func ThemeExists(name string) bool {
	_, ok := themeRegistry[name]
	return ok
}

// ThemeMenuData returns the ordered labels plus label<->key maps for building
// the terminal-theme selector. Order is registry order (built-ins first).
func ThemeMenuData() (labels []string, labelToKey, keyToLabel map[string]string) {
	labelToKey = map[string]string{}
	keyToLabel = map[string]string{}
	seen := map[string]bool{}
	for _, name := range themeOrder {
		t := themeRegistry[name]
		if t == nil {
			continue
		}
		label := t.Label
		if label == "" {
			label = name
		}
		// Disambiguate the rare label collision so the maps stay 1:1.
		if seen[label] {
			label = label + " (" + name + ")"
		}
		seen[label] = true
		labels = append(labels, label)
		labelToKey[label] = name
		keyToLabel[name] = label
	}
	return labels, labelToKey, keyToLabel
}

// --- color resolution helpers ------------------------------------------------

// hexOr parses a hex string, returning fallback when empty/invalid.
func hexOr(hex string, fallback color.Color) color.Color {
	if c := ParseHexColor(hex); c != nil {
		return c
	}
	return fallback
}

// xterm256BaseNames maps the first 16 xterm-256 indices to palette keys, so the
// low 16 follow the terminal palette rather than fixed RGB. Indices 16-255
// are absolute (cube + grayscale) and computed directly in Xterm256Color.
var xterm256BaseNames = [16]string{
	"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
	"bright_black", "bright_red", "bright_green", "bright_yellow",
	"bright_blue", "bright_magenta", "bright_cyan", "bright_white",
}

// terminalColorMap materializes a theme's terminal palette as the ANSI-name ->
// color map the renderer expects, including the "default" (= foreground) key.
func (t *ThemeDef) terminalColorMap() map[string]color.Color {
	x := t.Terminal
	fg := hexOr(x.Foreground, color.RGBA{0xe0, 0xe0, 0xe0, 0xff})
	return map[string]color.Color{
		"black":          hexOr(x.Black, color.RGBA{0x00, 0x00, 0x00, 0xff}),
		"red":            hexOr(x.Red, color.RGBA{0xcc, 0x00, 0x00, 0xff}),
		"green":          hexOr(x.Green, color.RGBA{0x00, 0xaa, 0x00, 0xff}),
		"yellow":         hexOr(x.Yellow, color.RGBA{0xaa, 0xaa, 0x00, 0xff}),
		"blue":           hexOr(x.Blue, color.RGBA{0x00, 0x00, 0xcc, 0xff}),
		"magenta":        hexOr(x.Magenta, color.RGBA{0xcc, 0x00, 0xcc, 0xff}),
		"cyan":           hexOr(x.Cyan, color.RGBA{0x00, 0xaa, 0xaa, 0xff}),
		"white":          hexOr(x.White, color.RGBA{0xaa, 0xaa, 0xaa, 0xff}),
		"bright_black":   hexOr(x.BrightBlack, color.RGBA{0x55, 0x55, 0x55, 0xff}),
		"bright_red":     hexOr(x.BrightRed, color.RGBA{0xff, 0x55, 0x55, 0xff}),
		"bright_green":   hexOr(x.BrightGreen, color.RGBA{0x55, 0xff, 0x55, 0xff}),
		"bright_yellow":  hexOr(x.BrightYellow, color.RGBA{0xff, 0xff, 0x55, 0xff}),
		"bright_blue":    hexOr(x.BrightBlue, color.RGBA{0x55, 0x55, 0xff, 0xff}),
		"bright_magenta": hexOr(x.BrightMagenta, color.RGBA{0xff, 0x55, 0xff, 0xff}),
		"bright_cyan":    hexOr(x.BrightCyan, color.RGBA{0x55, 0xff, 0xff, 0xff}),
		"bright_white":   hexOr(x.BrightWhite, color.RGBA{0xff, 0xff, 0xff, 0xff}),
		"default":        fg,
	}
}

// Xterm256Color decodes an xterm-256 palette index into a concrete color for
// the indices that are the same under every palette:
//   - 16-231: the 6x6x6 color cube (levels 0,95,135,175,215,255 per channel)
//   - 232-255: the 24-step grayscale ramp (8..238)
//
// Indices 0-15 are palette-dependent and must be resolved per widget, so they
// return nil here; callers use NativeTerminalWidget.termXterm256, which handles
// the low 16 through that terminal's own palette and defers here above it.
// Out-of-range indices return nil (treated as "default").
func Xterm256Color(n int) color.Color {
	switch {
	case n < 16:
		return nil
	case n <= 231:
		n -= 16
		level := func(v int) uint8 {
			if v == 0 {
				return 0
			}
			return uint8(55 + v*40)
		}
		return color.RGBA{level(n / 36), level((n % 36) / 6), level(n % 6), 0xff}
	case n <= 255:
		g := uint8(8 + (n-232)*10)
		return color.RGBA{g, g, g, 0xff}
	default:
		return nil
	}
}

// --- embedded theme pack -----------------------------------------------------

// bundledThemes is the theme pack compiled into the binary (internal/ui/themes/*.yaml).
//
//go:embed themes/*.yaml
var bundledThemes embed.FS

// parseThemeYAML unmarshals one theme definition, defaulting the name from the
// filename (e.g. doom.yaml -> "doom") so a file without an explicit `name:`
// still registers. Returns nil (after logging) on a parse error.
func parseThemeYAML(filename string, data []byte) *ThemeDef {
	var def ThemeDef
	if err := yaml.Unmarshal(data, &def); err != nil {
		log.Printf("theme: skip %s: parse error: %v", filename, err)
		return nil
	}
	if def.Name == "" {
		def.Name = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	if def.Label == "" {
		def.Label = def.Name
	}
	return &def
}

// loadEmbeddedThemes registers the compiled-in theme pack. Called from init
// after the hardcoded built-ins, and before LoadUserThemes runs at startup, so
// user files on disk still layer over everything.
func loadEmbeddedThemes() {
	entries, err := bundledThemes.ReadDir("themes")
	if err != nil {
		log.Printf("theme: embedded pack unavailable: %v", err)
		return
	}
	for _, e := range entries {
		data, err := bundledThemes.ReadFile("themes/" + e.Name())
		if err != nil {
			log.Printf("theme: skip embedded %s: %v", e.Name(), err)
			continue
		}
		if def := parseThemeYAML(e.Name(), data); def != nil {
			registerTheme(def)
		}
	}
}

// --- user theme loading ------------------------------------------------------

// GetThemesDir returns ~/.pathfinderssh/themes, creating it if needed.
func GetThemesDir() string {
	dir := filepath.Join(GetAppHome(), "themes")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("Warning: could not create themes dir %s: %v", dir, err)
	}
	return dir
}

// LoadUserThemes scans the themes directory and registers every valid *.yaml /
// *.yml file, layering over the built-ins. Call once at startup, after
// GetAppHome is usable and before the first theme lookup.
func LoadUserThemes() {
	dir := GetThemesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // no dir / unreadable: built-ins only
	}
	loaded := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("theme: skip %s: %v", e.Name(), err)
			continue
		}
		def := parseThemeYAML(e.Name(), data)
		if def == nil {
			continue
		}
		registerTheme(def)
		loaded++
	}
	if loaded > 0 {
		log.Printf("theme: loaded %d user theme(s) from %s", loaded, dir)
	}
}

// --- built-in themes ---------------------------------------------------------
//
// Compiled-in palettes, so a binary whose embedded pack failed to read still
// has something to render with. ThemeCyber is also GetThemeDef's fallback, so
// it must always be registered.

// Built-in terminal palette names. These name TERMINAL palettes only; the
// application chrome is AppDark / AppLight and is set separately.
const (
	ThemeCyber     = "cyber"     // dark terminal, the fallback palette
	ThemeLight     = "light"     // light terminal
	ThemeCorporate = "corporate" // navy terminal
)

// DefaultTerminalTheme is the shipped terminal palette, from the embedded pack.
// It is a LIGHT palette under the dark default chrome, which is the pairing the
// two-independent-settings design exists to allow.
const DefaultTerminalTheme = "ice"

var builtinThemes = map[string]*ThemeDef{
	ThemeCyber: {
		Name: ThemeCyber, Label: "Cyber (Dark)", Type: "dark",
		Terminal: ThemeTerminal{
			Background: "#000510", Foreground: "#e0e0e0", Cursor: "#e0e0e0", Selection: "#003344",
			Black: "#000000", Red: "#ff0000", Green: "#00ff00", Yellow: "#ffff00",
			Blue: "#0000ff", Magenta: "#ff00ff", Cyan: "#00ffff", White: "#ffffff",
			BrightBlack: "#7f7f7f", BrightRed: "#ff5f5f", BrightGreen: "#5fff5f", BrightYellow: "#ffff5f",
			BrightBlue: "#5f5fff", BrightMagenta: "#ff5fff", BrightCyan: "#5fffff", BrightWhite: "#ffffff",
		},
	},
	ThemeLight: {
		Name: ThemeLight, Label: "Light", Type: "light",
		Terminal: ThemeTerminal{
			Background: "#ffffff", Foreground: "#2e3440", Cursor: "#2e3440", Selection: "#0078d433",
			Black: "#000000", Red: "#cc0000", Green: "#008000", Yellow: "#808000",
			Blue: "#0000cc", Magenta: "#800080", Cyan: "#008080", White: "#808080",
			BrightBlack: "#545454", BrightRed: "#ff0000", BrightGreen: "#00aa00", BrightYellow: "#aaaa00",
			BrightBlue: "#0000ff", BrightMagenta: "#aa00aa", BrightCyan: "#00aaaa", BrightWhite: "#000000",
		},
	},
	ThemeCorporate: {
		Name: ThemeCorporate, Label: "Corporate", Type: "dark",
		Terminal: ThemeTerminal{
			Background: "#1b2a4b", Foreground: "#e0e0e0", Cursor: "#e0e0e0", Selection: "#0891b3",
			Black: "#000000", Red: "#ff0000", Green: "#00ff00", Yellow: "#ffff00",
			Blue: "#0000ff", Magenta: "#ff00ff", Cyan: "#00ffff", White: "#ffffff",
			BrightBlack: "#7f7f7f", BrightRed: "#ff5f5f", BrightGreen: "#5fff5f", BrightYellow: "#ffff5f",
			BrightBlue: "#5f5fff", BrightMagenta: "#ff5fff", BrightCyan: "#5fffff", BrightWhite: "#ffffff",
		},
	},
}

func init() {
	// Register built-ins in a stable, friendly order.
	for _, n := range []string{ThemeCyber, ThemeLight, ThemeCorporate} {
		registerTheme(builtinThemes[n])
	}
	// Layer the embedded theme pack over the built-ins. User themes from
	// ~/.pathfinderssh/themes layer over both, at LoadUserThemes() in main.
	loadEmbeddedThemes()
}
