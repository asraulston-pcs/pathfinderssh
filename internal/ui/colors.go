// internal/ui/colors.go
// Hex color parsing for the theme layer.
//
// This lived in TetherSSH's settings.go, a 1,040-line application-settings blob
// that is not being ported. It also used to hold ColorOverrides, a 13-slot
// per-theme override bucket for the application chrome palette; that went when
// the chrome collapsed to light/dark, since every slot it addressed
// (primary/surface/input_border/...) no longer exists. Terminal palettes are
// customised by editing or adding a theme YAML, which is what the registry is
// for.
package ui

import (
	"image/color"
	"strconv"
	"strings"
)

// ParseHexColor parses #RGB, #RRGGBB or #RRGGBBAA, with or without the leading
// hash. It returns nil on anything it does not understand, which every caller
// treats as "no override" rather than as an error.
func ParseHexColor(hex string) color.Color {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")

	switch len(h) {
	case 3: // #RGB -> #RRGGBB
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	case 6, 8:
	default:
		return nil
	}

	v, err := strconv.ParseUint(h, 16, 64)
	if err != nil {
		return nil
	}

	if len(h) == 6 {
		return color.RGBA{
			R: uint8(v >> 16),
			G: uint8(v >> 8),
			B: uint8(v),
			A: 0xff,
		}
	}
	return color.RGBA{
		R: uint8(v >> 24),
		G: uint8(v >> 16),
		B: uint8(v >> 8),
		A: uint8(v),
	}
}
