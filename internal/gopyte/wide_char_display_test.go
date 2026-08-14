// internal/gopyte/widechar_display_test.go
//
// Separates model correctness from display correctness for double-width
// glyphs.
//
// The model records a wide rune plus a width-0 continuation cell. The display
// serialization must preserve one entry per column -- rune index == column
// index -- because the renderer, the background layer and the selection
// hit-test all map rune index directly to grid column. Text consumers want
// the collapsed form instead, which is what the *Text accessors provide.
//
// This file deliberately lives in package gopyte so it exercises the in-tree
// fork, not the published module.
package gopyte

import (
	"strings"
	"testing"
)

const wideSample = "|日本語|" // 5 runes, 8 columns

// --- display side: one entry per column -------------------------------------

func TestWideCharCursorAccounting(t *testing.T) {
	s := NewWideCharScreen(20, 5, 100)
	s.Draw(wideSample)

	if cx, _ := s.GetCursor(); cx != 8 {
		t.Errorf("cursor column = %d, want 8", cx)
	}
}

func TestWideCharDisplayColumns(t *testing.T) {
	s := NewWideCharScreen(20, 5, 100)
	s.Draw(wideSample)

	// Rows are right-trimmed of blanks by the display layer (the renderer
	// re-pads to the grid width), so the invariant to assert is that every
	// column up to end-of-content maps to exactly one rune.
	row := []rune(s.GetDisplay()[0])
	if len(row) != 8 {
		t.Fatalf("display row has %d runes, want 8 (one per occupied column)", len(row))
	}
	if row[7] != '|' {
		t.Errorf("column 7 = %q, want the closing pipe", row[7])
	}
	for _, c := range []int{2, 4, 6} {
		if row[c] != ContinuationRune {
			t.Errorf("column %d = %q, want a continuation spacer", c, row[c])
		}
	}
}

func TestWideCharAttrsAlignment(t *testing.T) {
	s := NewWideCharScreen(20, 5, 100)
	s.Draw(wideSample)

	if got := len(s.GetAttributes()[0]); got != 20 {
		t.Errorf("attribute row has %d entries, want 20 (one per column; "+
			"attributes are not trimmed)", got)
	}
}

func TestWideCharEdgeOfLine(t *testing.T) {
	s := NewWideCharScreen(5, 3, 100)
	s.Draw("abcd日")

	cx, cy := s.GetCursor()
	if cy != 1 || cx != 2 {
		t.Errorf("cursor = (%d,%d), want (2,1): a wide glyph must wrap whole", cx, cy)
	}
}

// --- text side: spacers collapsed -------------------------------------------

func TestWideCharTextExtraction(t *testing.T) {
	s := NewWideCharScreen(20, 5, 100)
	s.Draw(wideSample)

	text := strings.TrimRight(s.GetDisplayText()[0], " ")
	if text != wideSample {
		t.Errorf("extracted text = %q, want %q", text, wideSample)
	}
	if strings.ContainsRune(text, ContinuationRune) {
		t.Error("extracted text still contains a continuation spacer")
	}
}

func TestStripContinuationsIsIdentityForASCII(t *testing.T) {
	const line = "show ip bgp summary"
	if got := StripContinuations(line); got != line {
		t.Errorf("StripContinuations(%q) = %q, want unchanged", line, got)
	}
}

// --- the common case must not regress ---------------------------------------

// Ordinary ASCII output is essentially all device CLI traffic, so an
// off-by-one here would be far worse than the bug being fixed.
func TestASCIIRowIsUnchanged(t *testing.T) {
	s := NewWideCharScreen(20, 5, 100)
	s.Draw("abcdef")

	row := []rune(s.GetDisplay()[0])
	if len(row) != 6 {
		t.Fatalf("display row has %d runes, want 6", len(row))
	}
	if got := string(row); got != "abcdef" {
		t.Errorf("row = %q, want %q", got, "abcdef")
	}
	if strings.ContainsRune(string(row), ContinuationRune) {
		t.Error("an ASCII row must contain no continuation spacers")
	}
}

// Braille is multibyte but single-width; it is what btop draws its meters
// with, and it must stay one rune per column.
func TestBrailleIsSingleWidth(t *testing.T) {
	s := NewWideCharScreen(20, 5, 100)
	s.Draw("⣿⣶⣤⣀")

	if cx, _ := s.GetCursor(); cx != 4 {
		t.Errorf("cursor column = %d, want 4", cx)
	}
	row := []rune(s.GetDisplay()[0])
	if strings.ContainsRune(string(row), ContinuationRune) {
		t.Error("Braille must not produce continuation spacers")
	}
}

// --- history path -----------------------------------------------------------

// The live screen and the scrollback share renderRow; a row that has scrolled
// into history must serialize identically to one still on screen.
func TestWideCharSurvivesScrollIntoHistory(t *testing.T) {
	s := NewWideCharScreen(20, 3, 100)
	s.Draw(wideSample)
	for i := 0; i < 5; i++ {
		s.Linefeed()
	}

	total := s.GetTotalContentLines()
	lines := s.GetLinesInRange(0, total)
	if len(lines) == 0 {
		t.Fatal("no lines returned")
	}

	row := []rune(lines[0])
	if len(row) != 8 {
		t.Fatalf("history row has %d runes, want 8 (one per occupied column)", len(row))
	}
	if row[7] != '|' {
		t.Errorf("column 7 = %q, want the closing pipe", row[7])
	}

	text := strings.TrimRight(s.GetTextInRange(0, total)[0], " ")
	if text != wideSample {
		t.Errorf("history text = %q, want %q", text, wideSample)
	}
}
