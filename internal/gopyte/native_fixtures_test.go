package gopyte_test

import (
	"os"
	"strings"
	"testing"

	gopyte "github.com/scottpeterman/pathfinderssh/internal/gopyte"
)

func TestNativeScreenWithFixtures(t *testing.T) {
	// Fixtures are optional; every case skips when testdata is absent.
	tryPaths := func(name string) string {
		paths := []string{
			"testdata/" + name + ".input",
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
		return ""
	}

	// Test files
	fixtures := []struct {
		name     string
		file     string
		validate func(t *testing.T, display []string)
	}{
		{
			name: "ls",
			file: tryPaths("ls"),
			validate: func(t *testing.T, display []string) {
				// Should have some file listings
				hasContent := false
				for _, line := range display {
					if strings.TrimSpace(line) != "" {
						hasContent = true
						break
					}
				}
				if !hasContent {
					t.Error("ls output should have content")
				}
			},
		},
		{
			name: "cat-gpl3",
			file: tryPaths("cat-gpl3"),
			validate: func(t *testing.T, display []string) {
				// Should contain GPL text
				hasGPL := false
				for _, line := range display {
					if strings.Contains(line, "GPL") || strings.Contains(line, "General Public License") {
						hasGPL = true
						break
					}
				}
				if !hasGPL {
					t.Error("GPL text not found in output")
				}
			},
		},
		{
			name: "top",
			file: tryPaths("top"),
			validate: func(t *testing.T, display []string) {
				// Top usually clears screen and shows at the top
				if len(display) == 0 {
					t.Error("top output is empty")
				}
			},
		},
		{
			name: "htop",
			file: tryPaths("htop"),
			validate: func(t *testing.T, display []string) {
				// htop shows system info
				hasContent := false
				for _, line := range display {
					if strings.TrimSpace(line) != "" {
						hasContent = true
						break
					}
				}
				if !hasContent {
					t.Error("htop output should have content")
				}
			},
		},
		{
			name: "vi",
			file: tryPaths("vi"),
			validate: func(t *testing.T, display []string) {
				// vi/vim shows tildes for empty lines typically
				if len(display) == 0 {
					t.Error("vi output is empty")
				}
			},
		},
		{
			name: "mc",
			file: tryPaths("mc"),
			validate: func(t *testing.T, display []string) {
				// Midnight Commander has panels
				if len(display) == 0 {
					t.Error("mc output is empty")
				}
			},
		},
	}

	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			if tt.file == "" {
				t.Skipf("Could not find fixture file for %s", tt.name)
				return
			}

			// Read the fixture file
			data, err := os.ReadFile(tt.file)
			if err != nil {
				t.Skipf("Could not read fixture file %s: %v", tt.file, err)
				return
			}

			// Create native screen
			screen := gopyte.NewNativeScreen(80, 24)
			stream := gopyte.NewStream(screen, false)

			// Feed the data
			stream.Feed(string(data))

			// Get the display
			display := screen.GetDisplay()

			// Log first few non-empty lines for debugging
			t.Logf("Successfully parsed %s (%d bytes)", tt.name, len(data))
			count := 0
			for i, line := range display {
				if line != "" {
					t.Logf("  Line %d: %q", i, line)
					count++
					if count >= 5 {
						break
					}
				}
			}

			// Run validation if provided
			if tt.validate != nil {
				tt.validate(t, display)
			}
		})
	}
}

func TestNativeScreenComplexSequences(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, screen *gopyte.NativeScreen)
	}{
		{
			name:  "SGR colors",
			input: "\x1b[31mRed\x1b[32mGreen\x1b[34mBlue\x1b[0mNormal",
			validate: func(t *testing.T, screen *gopyte.NativeScreen) {
				display := screen.GetDisplay()
				if !strings.Contains(display[0], "RedGreenBlueNormal") {
					t.Errorf("Text not rendered correctly: %q", display[0])
				}
				// Check that attributes were set (would need getter methods)
			},
		},
		{
			name: "Scroll regions",
			input: "\x1b[5;10r" + // Set margins 5-10
				"\x1b[10;1H" + // Go to line 10
				"Bottom\n" + // Should scroll within region
				"Scrolled",
			validate: func(t *testing.T, screen *gopyte.NativeScreen) {
				display := screen.GetDisplay()
				// Line 9 (0-indexed) should have "Bottom"
				// Line 10 should have "Scrolled" after scroll
				t.Logf("After scroll region test:")
				for i := 4; i <= 10 && i < len(display); i++ {
					if display[i] != "" {
						t.Logf("  Line %d: %q", i, display[i])
					}
				}
			},
		},
		{
			name: "Save/Restore cursor",
			input: "\x1b[10;20H" + // Move to 10,20
				"\x1b7" + // Save
				"\x1b[1;1H" + // Move to 1,1
				"Home" +
				"\x1b8" + // Restore
				"Restored",
			validate: func(t *testing.T, screen *gopyte.NativeScreen) {
				display := screen.GetDisplay()
				// Should have "Home" at position 0,0
				if !strings.HasPrefix(display[0], "Home") {
					t.Errorf("Home not at start: %q", display[0])
				}
				// Should have "Restored" at line 9, col 19
				if len(display) > 9 {
					t.Logf("Line 9: %q", display[9])
				}
			},
		},
		{
			name: "Insert/Delete lines",
			input: "Line1\r\nLine2\r\nLine3" +
				"\x1b[2;1H" + // Go to line 2
				"\x1b[L" + // Insert line
				"Inserted" +
				"\x1b[4;1H" + // Go to line 4
				"\x1b[M", // Delete line
			validate: func(t *testing.T, screen *gopyte.NativeScreen) {
				display := screen.GetDisplay()
				for i := 0; i < 5 && i < len(display); i++ {
					if display[i] != "" {
						t.Logf("Line %d: %q", i, display[i])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			screen := gopyte.NewNativeScreen(80, 24)
			stream := gopyte.NewStream(screen, false)

			stream.Feed(tt.input)

			if tt.validate != nil {
				tt.validate(t, screen)
			}
		})
	}
}
