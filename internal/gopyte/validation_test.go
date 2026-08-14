package gopyte_test

import (
	"strings"
	"testing"

	gopyte "github.com/scottpeterman/pathfinderssh/internal/gopyte"
)

func TestMockScreenValidation(t *testing.T) {
	testCases := []struct {
		name        string
		input       string
		mustContain []string // Changed from exact matches to substring matches
	}{
		{
			name:        "Simple text",
			input:       "Hello",
			mustContain: []string{"Draw", "Hello"},
		},
		{
			name:        "Text with newline",
			input:       "Line1\nLine2",
			mustContain: []string{"Line1", "Linefeed", "Line2"},
		},
		{
			name:        "CRLF",
			input:       "Test\r\n",
			mustContain: []string{"Test", "CarriageReturn", "Linefeed"},
		},
		{
			name:  "Cursor movement",
			input: "\x1b[5A\x1b[3B\x1b[2C\x1b[4D",
			mustContain: []string{
				"CursorUp[5]",
				"CursorDown[3]",
				"CursorForward[2]",
				"CursorBack[4]",
			},
		},
		{
			name:        "SGR sequence",
			input:       "\x1b[1;31;44m",
			mustContain: []string{"SelectGraphicRendition[[1 31 44]]"},
		},
		{
			name:        "Clear and home",
			input:       "\x1b[2J\x1b[H",
			mustContain: []string{"EraseInDisplay[2]", "CursorPosition[1 1]"},
		},
		{
			name:        "Set margins",
			input:       "\x1b[5;20r",
			mustContain: []string{"SetMargins[5 20]"},
		},
		{
			name:        "Private mode set",
			input:       "\x1b[?25h",
			mustContain: []string{"SetMode[[25] true]"},
		},
		{
			name:  "Multiple SGR",
			input: "\x1b[0;1;31mRed Bold\x1b[m",
			mustContain: []string{
				"SelectGraphicRendition[[0 1 31]]",
				"Red Bold", // Just check the text is there
				"SelectGraphicRendition[[0]]",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			screen := gopyte.NewMockScreen()
			stream := gopyte.NewStream(screen, false)

			stream.Feed(tc.input)

			// Join all calls for easier searching
			allCalls := strings.Join(screen.Calls, " ")

			// Check that all expected substrings are present
			for _, expected := range tc.mustContain {
				if !strings.Contains(allCalls, expected) {
					t.Errorf("Expected substring %q not found.\nGot calls: %v", expected, screen.Calls)
				}
			}
		})
	}
}

func TestComplexSequences(t *testing.T) {
	screen := gopyte.NewMockScreen()
	stream := gopyte.NewStream(screen, false)

	// Test a complex real-world sequence (simplified vim startup)
	stream.Feed("\x1b[?1049h") // Alternative screen buffer
	stream.Feed("\x1b[1;24r")  // Set scrolling region
	stream.Feed("\x1b[?12h")   // Start blinking cursor
	stream.Feed("\x1b[?25h")   // Show cursor
	stream.Feed("\x1b[27m")    // Exit reverse video
	stream.Feed("\x1b[m")      // Reset attributes
	stream.Feed("\x1b[H")      // Home
	stream.Feed("\x1b[2J")     // Clear screen

	// Join all calls for substring matching
	allCalls := strings.Join(screen.Calls, " ")

	// Verify we handled all the sequences
	wantContains(t, allCalls, "SetMode[[1049] true]")
	wantContains(t, allCalls, "SetMargins[1 24]")
	wantContains(t, allCalls, "CursorPosition[1 1]")
	wantContains(t, allCalls, "EraseInDisplay[2]")
}

func TestTextBatching(t *testing.T) {
	screen := gopyte.NewMockScreen()
	stream := gopyte.NewStream(screen, false)

	// Feed a longer string without control characters
	stream.Feed("This is a longer test string")

	// Should be batched into one Draw call
	// Fatal, not Error: the next line indexes Calls[0], and testify's assert
	// would have let an empty slice through to a panic instead of a failure.
	if got := len(screen.Calls); got != 1 {
		t.Fatalf("expected 1 batched Draw call, got %d: %v", got, screen.Calls)
	}
	wantContains(t, screen.Calls[0], "This is a longer test string")

	// Test mixed content
	screen.Calls = nil
	stream.Feed("Start\x1b[31mRed\x1b[0mEnd")

	// Check all parts are there
	allCalls := strings.Join(screen.Calls, " ")
	wantContains(t, allCalls, "Start")
	wantContains(t, allCalls, "Red")
	wantContains(t, allCalls, "End")
	wantContains(t, allCalls, "SelectGraphicRendition[[31]]")
	wantContains(t, allCalls, "SelectGraphicRendition[[0]]")
}

// wantContains reports a missing substring with the full haystack, which is what
// makes a failure here diagnosable: the interesting information is the sequence
// of calls the stream actually produced, not the one that was expected.
func wantContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("missing %q in: %s", needle, haystack)
	}
}
